package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"

	"github.com/horchestra/pack-operator/authz"
	"github.com/horchestra/pack-operator/bus"
	opconfig "github.com/horchestra/pack-operator/config"
	"github.com/horchestra/pack-operator/db"
	opgit "github.com/horchestra/pack-operator/git"
)

// kv is the fixture pack: one setting, no hooks — the smallest pack the sdk writes.
type kv struct {
	Value string `json:"value"`
}

func (*kv) SetDefaults()              {}
func (*kv) Meta() sdk.Meta            { return sdk.Meta{Name: "kv", Version: "1.0.0"} }
func (*kv) PackRoot() packschema.Root { return packschema.Root{} }

// packBin is the pack binary, built once for the package's tests.
var packBin = sync.OnceValues(func() (string, error) {
	bin := filepath.Join(os.TempDir(), "pack-policy-test")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/pack")
	cmd.Dir = filepath.Join("..", "..") // the pack repository's root
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%s: %w", out, err)
	}
	return bin, nil
})

// fixture is a project repository: packs/kv, releases/prod/db (and releases/stage/db), a bare remote.
type fixture struct {
	t    *testing.T
	bare string
	seed *git.Repository
	dir  string
	head string
}

func newFixture(t *testing.T, env map[string]any) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, bare: filepath.Join(root, "repo.git"), dir: filepath.Join(root, "seed")}
	seed, err := git.PlainInit(f.dir, false)
	if err != nil {
		t.Fatal(err)
	}
	f.seed = seed
	if err := sdk.Generate(filepath.Join(f.dir, "packs"), &kv{}); err != nil {
		t.Fatal(err)
	}
	f.write("releases/prod/db/release.yaml",
		"pack: ../../../packs/kv\nstate: db\nid: 11111111-2222-3333-4444-555555555555\n")
	f.write("releases/prod/db/values.yaml", "value: one\n")
	f.writeYAML("releases/prod/db/env.yaml", env)
	f.write("releases/stage/db/release.yaml",
		"pack: ../../../packs/kv\nstate: db\nid: 66666666-7777-4888-8999-000000000000\n")
	f.write("releases/stage/db/values.yaml", "value: stage\n")
	f.commit("the start")
	if _, err := git.PlainInit(f.bare, true); err != nil {
		t.Fatal(err)
	}
	f.push("refs/heads/master:refs/heads/main")
	b, err := git.PlainOpen(f.bare)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) write(name, content string) {
	f.t.Helper()
	p := filepath.Join(f.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) writeYAML(name string, v any) {
	f.t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		f.t.Fatal(err)
	}
	// JSON is YAML: the fixture's env.yaml is simplest written so
	f.write(name, buf.String())
}

func (f *fixture) commit(message string) string {
	f.t.Helper()
	w, err := f.seed.Worktree()
	if err != nil {
		f.t.Fatal(err)
	}
	if err := w.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		f.t.Fatal(err)
	}
	h, err := w.Commit(message, &git.CommitOptions{Author: &object.Signature{Name: "alice", Email: "alice@example.org"}})
	if err != nil {
		f.t.Fatal(err)
	}
	f.head = h.String()
	return f.head
}

func (f *fixture) push(refspec string) {
	f.t.Helper()
	seed, err := git.PlainOpen(f.dir)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := seed.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{f.bare}}); err != nil {
		if _, err2 := seed.Remote("origin"); err2 != nil {
			f.t.Fatalf("the remote: %v, %v", err, err2)
		}
	}
	if err := seed.Push(&git.PushOptions{RefSpecs: []config.RefSpec{config.RefSpec(refspec)}}); err != nil {
		f.t.Fatal(err)
	}
}

// rig is a policy wired to a SQLite Bus, an authz, a fixture repository — and a worker running the jobs
// with the real pack binary, the native way (B6 takes this over).
type rig struct {
	t      *testing.T
	bus    *bus.SQLBus
	pol    *Policy
	repo   *opgit.Repo // the fixture's clone: the policy's Git and the worker's checkouts
	mu     sync.Mutex  // one checkout/fetch at a time
	cancel context.CancelFunc
	state  string // the DSN of the shared pack state store (К2): the jobs' -C dirs are throwaway
	cache  string // the shared PACK_CACHE (К1): a pack installed from a checkout is read from here again
}

func newRig(t *testing.T, f *fixture, gates []opconfig.Gate) *rig {
	t.Helper()
	bin, err := packBin()
	if err != nil {
		t.Fatalf("the pack binary: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d, err := db.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "operator.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	az, err := authz.New(d, "none", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := az.SeedProject("dev"); err != nil {
		t.Fatal(err)
	}
	b := bus.NewSQL(d)
	repo, err := opgit.Clone(ctx, "file://"+f.bare, filepath.Join(t.TempDir(), "clone"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	r := &rig{t: t, bus: b, repo: repo, cancel: cancel,
		state: "sqlite://" + filepath.Join(t.TempDir(), "state.db"), cache: t.TempDir()}
	cfg := &opconfig.Config{Projects: []opconfig.Project{{ID: "dev", Repo: "file://" + f.bare, Branch: "main",
		Runtime: "native", Gates: gates}}}
	r.pol = New(cfg, b, d, az, r)
	go r.pol.Run(ctx)
	go r.work(ctx, bin)
	return r
}

// Checkout is the policy's Git: the tree of the fixture at sha.
func (r *rig) Checkout(ctx context.Context, project, sha, dest string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.repo.Fetch(ctx); err != nil {
		return err
	}
	return r.repo.Checkout(ctx, sha, dest)
}

// work is the native executor in miniature: a queued job's argv runs in a checkout of its request's sha,
// --stream swapped for --json (the stream's events are B6's); the document becomes the job's result.
// A gate job (pack test --release) is answered without pack: the fixture's packs hold no scenarios, and the
// gate's order in the pipeline is what the tests check.
func (r *rig) work(ctx context.Context, bin string) {
	sub, err := r.bus.Subscribe(ctx, "worker", 0)
	if err != nil {
		return
	}
	for {
		m, err := sub.Next(ctx)
		if err != nil {
			return
		}
		if m.Kind == bus.KindJob {
			var job bus.Job
			if err := m.Decode(&job); err == nil && job.State == bus.JobQueued {
				r.exec(ctx, bin, job)
			}
		}
		_ = sub.Ack(ctx, m.Seq)
	}
}

func (r *rig) exec(ctx context.Context, bin string, job bus.Job) {
	req, err := r.bus.GetRequest(ctx, job.Request)
	if err != nil {
		return
	}
	job.Worker = "mini"
	job.Started = time.Now().UTC()
	var out []byte
	code := 0
	if slices.Contains(job.Argv, "test") {
		out = []byte(`{"ok": true}`) // a gate: no scenarios in the fixture
	} else {
		r.mu.Lock()
		_ = r.repo.Fetch(ctx)
		dest, err := os.MkdirTemp("", "pack-operator-job-")
		if err == nil {
			err = r.repo.Checkout(ctx, req.SHA, dest)
		}
		r.mu.Unlock()
		if err != nil {
			code = 1
			out = []byte(`{"error": "checkout: ` + err.Error() + `"}`)
		} else {
			// the checkout stays until the test ends: the state record names the pack's directory by its
			// absolute path, and a later job (a delete) reads the installed pack from there
			r.t.Cleanup(func() { os.RemoveAll(dest) })
			home := filepath.Join(dest, ".home")
			_ = os.MkdirAll(home, 0o700)
			args := slices.Clone(job.Argv[1:])
			for i, a := range args {
				if a == "--stream" {
					args[i] = "--json"
				}
			}
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Dir = dest
			cmd.Env = append(os.Environ(),
				"PACK_REVISION="+req.SHA, "HOME="+home, "PACK_CACHE="+r.cache, "PACK_LOG_LEVEL=error",
				"PACK_STATE_DSN="+r.state)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			out = bytes.TrimSpace(stdout.Bytes())
			if err != nil {
				code = 1
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					code = exit.ExitCode()
				}
				if len(out) == 0 {
					msg := strings.TrimSpace(stderr.String())
					if i := strings.LastIndex(msg, "\n"); i >= 0 {
						msg = msg[i+1:]
					}
					raw, _ := json.Marshal(map[string]string{"error": msg})
					out = raw
				}
			}
		}
	}
	if len(out) == 0 {
		out = []byte(`{}`)
	}
	job.Result = json.RawMessage(out)
	job.Exit = code
	job.Ended = time.Now().UTC()
	job.State = bus.JobSucceeded
	if code != 0 {
		job.State = bus.JobFailed
	}
	if err := r.bus.PublishJob(ctx, job); err != nil {
		r.t.Errorf("the job's result: %v", err)
	}
}

// submit puts a request on the Bus.
func (r *rig) submit(t *testing.T, sha, release, action string) bus.Request {
	t.Helper()
	req, err := bus.NewRequest("dev", sha, bus.Target{Release: release}, action, bus.Params{}, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.bus.Submit(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	return req
}

// stateOf polls the request's state until it is one of want, or the time runs out.
func (r *rig) stateOf(id string, want ...string) bus.Request {
	r.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		req, err := r.pol.request(context.Background(), id)
		if err == nil && slices.Contains(want, req.State) {
			return req
		}
		time.Sleep(50 * time.Millisecond)
	}
	req, _ := r.pol.request(context.Background(), id)
	for _, j := range r.jobsOf(id) {
		r.t.Logf("job %s (%s) exit %d: %s", j.ID, j.State, j.Exit, j.Result)
	}
	r.t.Fatalf("request %s: %s, waited for %s", id, req.State, strings.Join(want, "|"))
	return req
}

// jobsOf are the request's jobs, as the projection holds them, in creation order.
func (r *rig) jobsOf(requestID string) []bus.Job {
	r.t.Helper()
	rows, err := r.pol.db.QueryContext(context.Background(),
		`select body from operator_jobs where request = ? order by rowid`, requestID)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var jobs []bus.Job
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			r.t.Fatal(err)
		}
		var j bus.Job
		if err := json.Unmarshal(raw, &j); err != nil {
			r.t.Fatal(err)
		}
		jobs = append(jobs, j)
	}
	return jobs
}

// digestOf is the digest the request waits with.
func (r *rig) digestOf(id string) string {
	r.t.Helper()
	d, err := r.pol.digest(context.Background(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return d
}

// run.approve: auto-safe, a plan without risks — the plan request applies on its own.
func TestAutoSafe(t *testing.T) {
	f := newFixture(t, map[string]any{"target": "memory"})
	r := newRig(t, f, nil)
	req := r.submit(t, f.head, "releases/prod/db", bus.ActionPlan)
	done := r.stateOf(req.ID, bus.RequestDone)
	if done.State != bus.RequestDone {
		t.Fatalf("the request: %s", done.State)
	}
	jobs := r.jobsOf(req.ID)
	if len(jobs) != 2 || !strings.Contains(strings.Join(jobs[0].Argv, " "), "plan") ||
		!strings.Contains(strings.Join(jobs[1].Argv, " "), "apply --approve sha256:") {
		t.Fatalf("the pipeline's jobs: %+v", jobs)
	}
	for _, j := range jobs {
		if j.State != bus.JobSucceeded {
			t.Errorf("job %s: %s, exit %d, %s", j.ID, j.State, j.Exit, j.Result)
		}
	}
}

// run.approve: plan — the request waits; a wrong digest and a user without the right are refused; a trailer's
// approval of the right sha and digest applies.
func TestApproval(t *testing.T) {
	f := newFixture(t, map[string]any{"target": "memory", "run": map[string]any{"approve": "plan"}})
	r := newRig(t, f, nil)
	req := r.submit(t, f.head, "releases/prod/db", bus.ActionPlan)
	r.stateOf(req.ID, bus.RequestWaitingApproval)
	digest := r.digestOf(req.ID)
	if len(digest) == 0 {
		t.Fatal("no digest on the waiting request")
	}
	// a user without the right
	if err := r.pol.ApproveAPI(context.Background(), "mallory", req.ID, digest, f.head); err == nil {
		t.Error("mallory approved")
	}
	// a digest that is not the plan's
	if err := r.pol.Approve(context.Background(), "dev", "releases/prod/db", "sha256:wrong", f.head, "alice <alice@example.org>"); err == nil {
		t.Error("a wrong digest approved")
	}
	// the trailer's approval
	if err := r.pol.Approve(context.Background(), "dev", "releases/prod/db", digest, f.head, "alice <alice@example.org>"); err != nil {
		t.Fatal(err)
	}
	done := r.stateOf(req.ID, bus.RequestDone)
	if done.State != bus.RequestDone {
		t.Fatalf("after the approval: %s", done.State)
	}
	jobs := r.jobsOf(req.ID)
	if len(jobs) != 2 || !strings.Contains(strings.Join(jobs[1].Argv, " "), "--approve "+digest) {
		t.Fatalf("the jobs: %+v", jobs)
	}
}

// A newer commit stales the request that waits for an approval.
func TestStale(t *testing.T) {
	f := newFixture(t, map[string]any{"target": "memory", "run": map[string]any{"approve": "plan"}})
	r := newRig(t, f, nil)
	first := r.submit(t, f.head, "releases/prod/db", bus.ActionPlan)
	r.stateOf(first.ID, bus.RequestWaitingApproval)
	f.write("releases/prod/db/values.yaml", "value: two\n")
	sha2 := f.commit("two")
	f.push("refs/heads/master:refs/heads/main")
	second := r.submit(t, sha2, "releases/prod/db", bus.ActionPlan)
	r.stateOf(first.ID, bus.RequestStale)
	r.stateOf(second.ID, bus.RequestWaitingApproval)
	if err := r.pol.Approve(context.Background(), "dev", "releases/prod/db", r.digestOf(second.ID), sha2, "alice <alice@example.org>"); err != nil {
		t.Fatal(err)
	}
	r.stateOf(second.ID, bus.RequestDone)
	if req, _ := r.pol.request(context.Background(), first.ID); req.State != bus.RequestStale {
		t.Errorf("the first request: %s, must stay stale", req.State)
	}
}

// A delete is a dry-run, an approval, the delete.
func TestDelete(t *testing.T) {
	f := newFixture(t, map[string]any{"target": "memory"})
	r := newRig(t, f, nil)
	plan := r.submit(t, f.head, "releases/prod/db", bus.ActionPlan)
	r.stateOf(plan.ID, bus.RequestDone)
	del := r.submit(t, f.head, "releases/prod/db", bus.ActionDelete)
	r.stateOf(del.ID, bus.RequestWaitingApproval)
	digest := r.digestOf(del.ID)
	jobs := r.jobsOf(del.ID)
	if len(jobs) != 1 || !strings.Contains(strings.Join(jobs[0].Argv, " "), "delete --dry-run") {
		t.Fatalf("the dry-run job: %+v", jobs)
	}
	if err := r.pol.Approve(context.Background(), "dev", "releases/prod/db", digest, f.head, "alice <alice@example.org>"); err != nil {
		t.Fatal(err)
	}
	r.stateOf(del.ID, bus.RequestDone)
	jobs = r.jobsOf(del.ID)
	if len(jobs) != 2 || !strings.Contains(strings.Join(jobs[1].Argv, " "), "delete --approve "+digest) {
		t.Fatalf("the delete job: %+v", jobs)
	}
}

// Two plan requests of one release: the second waits for the first's pipeline, then runs its own.
func TestQueue(t *testing.T) {
	f := newFixture(t, map[string]any{"target": "memory"})
	bin, err := packBin()
	if err != nil {
		t.Fatalf("the pack binary: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := db.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "operator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	az, err := authz.New(d, "none", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := az.SeedProject("dev"); err != nil {
		t.Fatal(err)
	}
	b := bus.NewSQL(d)
	repo, err := opgit.Clone(ctx, "file://"+f.bare, filepath.Join(t.TempDir(), "clone"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	r := &rig{t: t, bus: b, repo: repo, cancel: cancel,
		state: "sqlite://" + filepath.Join(t.TempDir(), "state.db"), cache: t.TempDir()}
	cfg := &opconfig.Config{Projects: []opconfig.Project{{ID: "dev", Repo: "file://" + f.bare, Branch: "main", Runtime: "native"}}}
	r.pol = New(cfg, b, d, az, r)
	go r.pol.Run(ctx)
	// no worker yet: the first request's plan job queues, the second request must wait
	first := r.submit(t, f.head, "releases/prod/db", bus.ActionPlan)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if jobs := r.jobsOf(first.ID); len(jobs) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first request's plan job never queued")
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.write("releases/prod/db/values.yaml", "value: two\n")
	sha2 := f.commit("two")
	f.push("refs/heads/master:refs/heads/main")
	second := r.submit(t, sha2, "releases/prod/db", bus.ActionPlan)
	time.Sleep(300 * time.Millisecond) // the policy saw the second request by now
	if jobs := r.jobsOf(second.ID); len(jobs) > 0 {
		t.Fatalf("the second request started while the release was busy: %+v", jobs)
	}
	go r.work(ctx, bin)
	r.stateOf(first.ID, bus.RequestDone)
	r.stateOf(second.ID, bus.RequestDone)
	firstJobs, secondJobs := r.jobsOf(first.ID), r.jobsOf(second.ID)
	if len(secondJobs) == 0 || secondJobs[0].Created.Before(firstJobs[len(firstJobs)-1].Ended) {
		t.Errorf("the second pipeline started at %s, the first ended at %s",
			secondJobs[0].Created, firstJobs[len(firstJobs)-1].Ended)
	}
}

// A gate of the project: the scenarios on the test release stand between the plan and the apply.
func TestGate(t *testing.T) {
	f := newFixture(t, map[string]any{"target": "memory"})
	r := newRig(t, f, []opconfig.Gate{{Release: "releases/prod/db", TestRelease: "releases/stage/db"}})
	req := r.submit(t, f.head, "releases/prod/db", bus.ActionPlan)
	r.stateOf(req.ID, bus.RequestDone)
	jobs := r.jobsOf(req.ID)
	if len(jobs) != 3 {
		t.Fatalf("the pipeline with a gate: %+v", jobs)
	}
	gate := strings.Join(jobs[1].Argv, " ")
	if !strings.Contains(gate, "test --release releases/stage/db") {
		t.Fatalf("the gate job: %s", gate)
	}
	if !strings.Contains(strings.Join(jobs[2].Argv, " "), "apply --approve sha256:") {
		t.Fatalf("the apply after the gate: %+v", jobs[2])
	}
}
