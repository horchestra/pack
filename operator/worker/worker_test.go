package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	ggconfig "github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"

	"github.com/horchestra/pack-operator/bus"
	"github.com/horchestra/pack-operator/config"
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
	bin := filepath.Join(os.TempDir(), "pack-worker-test")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/pack")
	cmd.Dir = filepath.Join("..", "..") // the pack repository's root
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%s: %w", out, err)
	}
	return bin, nil
})

// fixture is a project repository: packs/kv, releases/prod/db on the memory target, a bare remote.
type fixture struct {
	t    *testing.T
	bare string
	head string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, bare: filepath.Join(root, "repo.git")}
	dir := filepath.Join(root, "seed")
	seed, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := sdk.Generate(filepath.Join(dir, "packs"), &kv{}); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("releases/prod/db/release.yaml",
		"pack: ../../../packs/kv\nstate: db\nid: 11111111-2222-3333-4444-555555555555\n")
	write("releases/prod/db/values.yaml", "value: one\n")
	write("releases/prod/db/env.yaml", `{"target": "memory"}`+"\n")
	w, err := seed.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	h, err := w.Commit("the start", &git.CommitOptions{Author: &object.Signature{Name: "alice", Email: "alice@example.org"}})
	if err != nil {
		t.Fatal(err)
	}
	f.head = h.String()
	if _, err := git.PlainInit(f.bare, true); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.CreateRemote(&ggconfig.RemoteConfig{Name: "origin", URLs: []string{f.bare}}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Push(&git.PushOptions{RefSpecs: []ggconfig.RefSpec{"refs/heads/master:refs/heads/main"}}); err != nil {
		t.Fatal(err)
	}
	b, err := git.PlainOpen(f.bare)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")); err != nil {
		t.Fatal(err)
	}
	return f
}

// rig is a Worker of the native runtime on a SQLite Bus and the fixture's repository.
type rig struct {
	t      *testing.T
	bus    *bus.SQLBus
	worker *Worker
	cache  string // the worker's PACK_CACHE of the project: it outlives every job
	jobs   string // the base of the jobs' temporary directories: empty when no job runs
}

func newRig(t *testing.T, f *fixture) *rig {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "operator.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	dataDir := t.TempDir()
	cfg := &config.Config{
		DataDir:  dataDir,
		StateDSN: "sqlite://" + filepath.Join(t.TempDir(), "state.db"),
		Projects: []config.Project{{ID: "dev", Repo: "file://" + f.bare, Branch: "main", Runtime: "native"}},
	}
	b := bus.NewSQL(d)
	repos := opgit.NewRepos(filepath.Join(dataDir, "repos"), nil)
	w := New(cfg, b, d, nil, repos, "native", nil, Native{Log: func(line string) { t.Log("pack:", line) }})
	return &rig{t: t, bus: b, worker: w, cache: filepath.Join(dataDir, "cache", "dev"),
		jobs: filepath.Join(dataDir, "jobs")}
}

// queue submits a request and its job of argv to the Bus.
func (r *rig) queue(t *testing.T, sha string, argv []string) bus.Job {
	t.Helper()
	ctx := context.Background()
	req, err := bus.NewRequest("dev", sha, bus.Target{Release: "releases/prod/db"}, bus.ActionPlan, bus.Params{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.bus.Submit(ctx, req); err != nil {
		t.Fatal(err)
	}
	job, err := bus.NewJob(req, argv, "native", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.bus.PublishJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	return job
}

// waitJob polls until the job is in one of the terminal states.
func (r *rig) waitJob(t *testing.T, id string) bus.Job {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		job, err := r.bus.GetJob(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		switch job.State {
		case bus.JobSucceeded, bus.JobFailed, bus.JobCanceled, bus.JobLost:
			return job
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the job never ended")
	return bus.Job{}
}

// events collects the job's job_event messages off the Bus.
func (r *rig) events(t *testing.T, jobID string) []bus.JobEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sub, err := r.bus.Subscribe(ctx, "test-"+jobID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	var out []bus.JobEvent
	for {
		m, err := sub.Next(ctx)
		if err != nil {
			return out
		}
		if m.Kind == bus.KindJobEvent {
			var ev bus.JobEvent
			if err := m.Decode(&ev); err == nil && ev.Job == jobID {
				out = append(out, ev)
			}
		}
		_ = sub.Ack(ctx, m.Seq)
	}
}

// A plan job runs to the end: succeeded, the events numbered without gaps or repeats from 1, the checkout and
// home removed, the worker's cache left (К6).
func TestNativePlan(t *testing.T) {
	f := newFixture(t)
	r := newRig(t, f)
	bin, err := packBin()
	if err != nil {
		t.Fatalf("the pack binary: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.worker.Run(ctx) }()
	job := r.queue(t, f.head, []string{bin, "-C", "releases/prod/db", "plan", "--stream"})
	done := r.waitJob(t, job.ID)
	if done.State != bus.JobSucceeded {
		t.Fatalf("the job: %s, exit %d, %s", done.State, done.Exit, done.Result)
	}
	if done.Worker != r.worker.id {
		t.Errorf("the worker: %q, want %q", done.Worker, r.worker.id)
	}
	events := r.events(t, job.ID)
	if len(events) == 0 {
		t.Fatal("no job_event records")
	}
	for i, ev := range events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d: seq %d — a gap or a repeat", i, ev.Seq)
		}
	}
	var raw struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(events[len(events)-1].Event, &raw); err != nil || raw.Type != "result" {
		t.Errorf("the last event is the result: %s", events[len(events)-1].Event)
	}
	if len(done.Result) == 0 {
		t.Errorf("the job's result is empty")
	}
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		left, _ := filepath.Glob(filepath.Join(r.jobs, "job-*"))
		if len(left) == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if left, _ := filepath.Glob(filepath.Join(r.jobs, "job-*")); len(left) > 0 {
		t.Errorf("the job's directory is left: %v", left)
	}
	if _, err := os.Stat(r.cache); err != nil {
		t.Errorf("the worker's cache is gone: %v", err)
	}
}

// A canceled job gets the runtime's interrupt (native: SIGINT) and ends canceled.
func TestNativeCancel(t *testing.T) {
	f := newFixture(t)
	r := newRig(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.worker.Run(ctx) }()
	job := r.queue(t, f.head, []string{"sh", "-c", "sleep 300"})
	// wait for running, then ask to cancel by the job record's state — the heartbeat's watch interrupts
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := r.bus.GetJob(context.Background(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if cur.State == bus.JobRunning {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cur, err := r.bus.GetJob(context.Background(), job.ID)
	if err != nil || cur.State != bus.JobRunning {
		t.Fatalf("the job never ran: %v, %+v", err, cur)
	}
	cur.State = bus.JobCanceled
	if err := r.bus.PublishJob(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	done := r.waitJob(t, job.ID)
	if done.State != bus.JobCanceled {
		t.Fatalf("the job: %s, exit %d", done.State, done.Exit)
	}
}

// A native job left running at the worker's restart is lost (К6: a native runtime keeps nothing).
func TestNativeRecoverLost(t *testing.T) {
	f := newFixture(t)
	r := newRig(t, f)
	job := r.queue(t, f.head, []string{"sh", "-c", "sleep 300"})
	cur, err := r.bus.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	cur.State = bus.JobRunning
	cur.Worker = r.worker.id
	if err := r.bus.PublishJob(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	if err := r.worker.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	done, err := r.bus.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != bus.JobLost {
		t.Fatalf("the recovered native job: %s, want lost", done.State)
	}
}
