// The worker's docker and k8s runtimes end to end (B6, К6): a real pack image runs a job's plan, a cancel
// interrupts, a worker restart reattaches by the labels and reads on from last_seq without gaps or repeats.
// Docker must answer; the k8s test raises its own kind cluster and never touches ~/.kube/config.
package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	ggconfig "github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"

	"github.com/horchestra/pack/operator/bus"
	"github.com/horchestra/pack/operator/config"
	"github.com/horchestra/pack/operator/db"
	opgit "github.com/horchestra/pack/operator/git"
	"github.com/horchestra/pack/operator/worker"
)

type kv struct {
	Value string `json:"value"`
}

func (*kv) SetDefaults()              {}
func (*kv) Meta() sdk.Meta            { return sdk.Meta{Name: "kv", Version: "1.0.0"} }
func (*kv) PackRoot() packschema.Root { return packschema.Root{} }

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

// rig is a Worker of one runtime on a SQLite Bus and the fixture's repository.
type rig struct {
	t      *testing.T
	bus    *bus.SQLBus
	worker *worker.Worker
}

func newRig(t *testing.T, f *fixture, runtime string, rt worker.Runtime) *rig {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "operator.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	dataDir := t.TempDir()
	cfg := &config.Config{
		DataDir: dataDir,
		// the state lives in the job's cache mount: every runtime sees it at /cache (К2)
		StateDSN: "sqlite:///cache/state.db",
		Projects: []config.Project{{ID: "dev", Repo: "file://" + f.bare, Branch: "main", Runtime: runtime}},
	}
	b := bus.NewSQL(d)
	repos := opgit.NewRepos(filepath.Join(dataDir, "repos"), nil)
	w := worker.New(cfg, b, d, nil, repos, runtime, nil, rt)
	return &rig{t: t, bus: b, worker: w}
}

func (r *rig) queue(t *testing.T, sha string, argv []string, runtime string) bus.Job {
	t.Helper()
	ctx := context.Background()
	req, err := bus.NewRequest("dev", sha, bus.Target{Release: "releases/prod/db"}, bus.ActionPlan, bus.Params{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.bus.Submit(ctx, req); err != nil {
		t.Fatal(err)
	}
	job, err := bus.NewJob(req, argv, runtime, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.bus.PublishJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	return job
}

func (r *rig) waitJob(t *testing.T, id string) bus.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		job, err := r.bus.GetJob(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		switch job.State {
		case bus.JobSucceeded, bus.JobFailed, bus.JobCanceled, bus.JobLost:
			return job
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("the job never ended")
	return bus.Job{}
}

func (r *rig) waitState(t *testing.T, id, state string) bus.Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		job, err := r.bus.GetJob(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State == state {
			return job
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("the job never reached %s", state)
	return bus.Job{}
}

// events collects the job's job_event messages off the Bus.
func (r *rig) events(t *testing.T, jobID string) []bus.JobEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

func dockerOK(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("no docker")
	}
}

// packImage is the pack:test image, built once from the repository's Dockerfile.
var packImage = sync.OnceValues(func() (string, error) {
	if err := exec.Command("docker", "image", "inspect", "pack:test").Run(); err == nil {
		return "pack:test", nil
	}
	cmd := exec.Command("docker", "build", "-t", "pack:test", ".")
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%s: %w", out, err)
	}
	return "pack:test", nil
})

// A docker job runs a plan in the pack image: succeeded, the events numbered, the container gone (К6).
func TestDockerPlan(t *testing.T) {
	dockerOK(t)
	if _, err := packImage(); err != nil {
		t.Fatalf("the pack image: %v", err)
	}
	f := newFixture(t)
	r := newRig(t, f, "docker", &worker.Docker{Image: "pack:test", Log: func(line string) { t.Log("pack:", line) }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.worker.Run(ctx) }()
	job := r.queue(t, f.head, []string{"pack", "-C", "releases/prod/db", "plan", "--stream"}, "docker")
	done := r.waitJob(t, job.ID)
	if done.State != bus.JobSucceeded {
		t.Fatalf("the job: %s, exit %d, %s", done.State, done.Exit, done.Result)
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
	out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=pack-operator.job="+job.ID).Output()
	if len(strings.TrimSpace(string(out))) > 0 {
		t.Errorf("the container is left: %s", out)
	}
}

// A canceled docker job gets docker kill --signal INT; a worker restarted over a running job reattaches by the
// label and reads on from last_seq — no gaps, no repeats (К6).
func TestDockerReattach(t *testing.T) {
	dockerOK(t)
	if err := exec.Command("docker", "pull", "alpine:3").Run(); err != nil {
		t.Skipf("alpine:3: %v", err)
	}
	f := newFixture(t)
	r := newRig(t, f, "docker", &worker.Docker{Image: "alpine:3"})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = r.worker.Run(ctx) }()
	job := r.queue(t, f.head, []string{"pack", "sh", "-c",
		`echo '{"type":"a"}'; sleep 2; echo '{"type":"b"}'; sleep 600`}, "docker")
	r.waitState(t, job.ID, bus.JobRunning)
	// the two events arrive, then the worker dies mid-job
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		cur, _ := r.bus.GetJob(context.Background(), job.ID)
		if cur.LastSeq >= 2 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	cur, _ := r.bus.GetJob(context.Background(), job.ID)
	if cur.LastSeq < 2 {
		t.Fatalf("last_seq before the restart: %d", cur.LastSeq)
	}
	cancel() // the worker is dead; the container runs on
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && r.worker.RunningJobs() > 0 {
		time.Sleep(100 * time.Millisecond)
	}
	// a new worker over the same Bus finds the job by the label and reads on
	r2 := newRig2(t, f, r)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = r2.worker.Run(ctx2) }()
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		cur, _ := r2.bus.GetJob(context.Background(), job.ID)
		if cur.State == bus.JobRunning && cur.Worker == r2.worker.ID() {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	// cancel: the interrupt is docker kill --signal INT
	cur, _ = r2.bus.GetJob(context.Background(), job.ID)
	cur.State = bus.JobCanceled
	if err := r2.bus.PublishJob(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	done := r2.waitJob(t, job.ID)
	if done.State != bus.JobCanceled {
		t.Fatalf("the reattached job: %s", done.State)
	}
	events := r2.events(t, job.ID)
	if len(events) != 2 {
		t.Fatalf("the events after the reattach: %d, want exactly the 2 — no gaps, no repeats", len(events))
	}
	for i, ev := range events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d: seq %d", i, ev.Seq)
		}
	}
}

// newRig2 is a second worker on the first rig's Bus and fixture (the restart).
func newRig2(t *testing.T, f *fixture, prev *rig) *rig {
	t.Helper()
	return &rig{t: t, bus: prev.bus, worker: worker.New(
		prev.worker.Config(), prev.bus, nil, nil, prev.worker.Repos(), "docker", nil,
		&worker.Docker{Image: "alpine:3"})}
}

// ---- k8s

// kindCluster raises a throwaway kind cluster with its own kubeconfig (never ~/.kube/config) and loads the
// locally built images the jobs need (public ones the node pulls itself). The second answer is the IP the
// test's host answers at from inside the cluster (the pod's hostAliases: a pod's DNS knows no
// host.docker.internal).
func kindCluster(t *testing.T, f *fixture, localImages ...string) (string, string) {
	t.Helper()
	if err := exec.Command("kind", "version").Run(); err != nil {
		t.Skip("no kind")
	}
	dockerOK(t)
	name := "pack-operator-e2e-" + strings.ToLower(strings.TrimPrefix(fmt.Sprintf("%x", time.Now().UnixNano()%0xFFFFFF), ""))
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	out, err := exec.Command("kind", "create", "cluster", "--name", name, "--kubeconfig", kubeconfig).CombinedOutput()
	if err != nil {
		t.Skipf("kind create cluster: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("kind", "delete", "cluster", "--name", name).Run()
	})
	// every image the jobs need comes from the local docker: a fresh node pulling from Docker Hub is minutes
	// (and rate-limited) under a full suite's load
	images := append([]string{"alpine/git:latest", "alpine:3"}, localImages...)
	for _, image := range images {
		if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
			if out, err := exec.Command("docker", "pull", image).CombinedOutput(); err != nil {
				t.Fatalf("pull %s: %v: %s", image, err, out)
			}
		}
		// docker save | ctr import: kind load (both flavors) adds --all-platforms, which a multi-arch
		// image of a containerd-backed docker store breaks; a plain import takes the node's platform
		save := exec.Command("docker", "save", image)
		load := exec.Command("docker", "exec", "-i", name+"-control-plane",
			"ctr", "--namespace=k8s.io", "images", "import", "-")
		pipe, err := save.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		load.Stdin = pipe
		if err := save.Start(); err != nil {
			t.Fatalf("docker save %s: %v", image, err)
		}
		if out, err := load.CombinedOutput(); err != nil {
			t.Fatalf("the node's import of %s: %v: %s", image, err, out)
		}
		if err := save.Wait(); err != nil {
			t.Fatalf("docker save %s: %v", image, err)
		}
	}
	// the host's address as the node sees it: host.docker.internal is the node's own /etc/hosts entry
	hostIP := "192.168.65.254"
	if out, err := exec.Command("docker", "exec", name+"-control-plane", "getent", "hosts", "host.docker.internal").Output(); err == nil {
		if fields := strings.Fields(string(out)); len(fields) > 0 {
			hostIP = fields[0]
		}
	}
	return kubeconfig, hostIP
}

// gitDaemon serves the fixture's bare repository over git:// for the k8s init container.
func gitDaemon(t *testing.T, f *fixture) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git binary for the fixture's daemon")
	}
	// a port of one's own: the previous test's daemon may hold its port still
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	base := filepath.Dir(f.bare)
	cmd := exec.Command("git", "daemon", "--reuseaddr", fmt.Sprintf("--port=%d", port),
		"--base-path="+base, "--export-all", base)
	if err := cmd.Start(); err != nil {
		t.Fatalf("git daemon: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return fmt.Sprintf("git://host.docker.internal:%d/%s", port, filepath.Base(f.bare))
}

// A k8s job runs a plan as a batch/v1 Job: the init container clones, pack plans, the events come numbered (К6).
func TestK8sPlan(t *testing.T) {
	if _, err := packImage(); err != nil {
		t.Fatalf("the pack image: %v", err)
	}
	f := newFixture(t)
	repoURL := gitDaemon(t, f)
	kubeconfig, hostIP := kindCluster(t, f, "pack:test")
	r := newRig(t, f, "k8s", &worker.K8s{Kubeconfig: kubeconfig, Image: "pack:test", HostIP: hostIP, RunAsUser: 1000})
	r.worker.SetRepoURL(repoURL) // inside kind the repository is the daemon's, not file://
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.worker.Run(ctx) }()
	job := r.queue(t, f.head, []string{"pack", "-C", "releases/prod/db", "plan", "--stream"}, "k8s")
	done := r.waitJob(t, job.ID)
	if done.State != bus.JobSucceeded {
		t.Fatalf("the job: %s, exit %d, %s", done.State, done.Exit, done.Result)
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
}

// dumpK8s logs the job's pods and their logs (a failure's diagnostics).
func dumpK8s(t *testing.T, r *rig, kubeconfig, jobID string) {
	t.Helper()
	out, err := exec.Command("kubectl", "--kubeconfig", kubeconfig, "get", "jobs,pods,secrets",
		"-l", "pack-operator.job="+jobID, "-o", "wide").CombinedOutput()
	t.Logf("objects: %v\n%s", err, out)
	out, err = exec.Command("kubectl", "--kubeconfig", kubeconfig, "describe", "job",
		"-l", "pack-operator.job="+jobID).CombinedOutput()
	t.Logf("describe: %v\n%s", err, out)
	out, err = exec.Command("kubectl", "--kubeconfig", kubeconfig, "logs",
		"-l", "pack-operator.job="+jobID, "--all-containers", "--tail", "20").CombinedOutput()
	t.Logf("logs: %v\n%s", err, out)
	job, jerr := r.bus.GetJob(context.Background(), jobID)
	t.Logf("the bus job: %+v (%v)", job, jerr)
}

// A canceled k8s job's Pod is deleted with the grace period; a restarted worker reattaches by the Job's labels
// and reads the log on from last_seq (К6).
func TestK8sReattach(t *testing.T) {
	f := newFixture(t)
	repoURL := gitDaemon(t, f)
	kubeconfig, hostIP := kindCluster(t, f)
	r := newRig(t, f, "k8s", &worker.K8s{Kubeconfig: kubeconfig, Image: "alpine:3", HostIP: hostIP})
	r.worker.SetRepoURL(repoURL)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = r.worker.Run(ctx) }()
	job := r.queue(t, f.head, []string{"pack", "sh", "-c",
		`echo '{"type":"a"}'; sleep 2; echo '{"type":"b"}'; sleep 600`}, "k8s")
	r.waitState(t, job.ID, bus.JobRunning)
	// a fresh cluster pulls the images first, and a loaded docker pulls slowly: the events may be a while
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		cur, _ := r.bus.GetJob(context.Background(), job.ID)
		if cur.LastSeq >= 2 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	cur, _ := r.bus.GetJob(context.Background(), job.ID)
	if cur.LastSeq < 2 {
		dumpK8s(t, r, kubeconfig, job.ID)
		t.Fatalf("last_seq before the restart: %d", cur.LastSeq)
	}
	cancel()
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && r.worker.RunningJobs() > 0 {
		time.Sleep(100 * time.Millisecond)
	}
	r2 := &rig{t: t, bus: r.bus, worker: worker.New(r.worker.Config(), r.bus, nil, nil, r.worker.Repos(), "k8s", nil,
		&worker.K8s{Kubeconfig: kubeconfig, Image: "alpine:3", HostIP: hostIP})}
	r2.worker.SetRepoURL(repoURL)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = r2.worker.Run(ctx2) }()
	time.Sleep(3 * time.Second) // the recovery reattaches
	cur, _ = r2.bus.GetJob(context.Background(), job.ID)
	cur.State = bus.JobCanceled
	if err := r2.bus.PublishJob(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	done := r2.waitJob(t, job.ID)
	if done.State != bus.JobCanceled {
		t.Fatalf("the reattached job: %s", done.State)
	}
	events := r2.events(t, job.ID)
	if len(events) != 2 {
		t.Fatalf("the events after the reattach: %d, want exactly the 2 — no gaps, no repeats", len(events))
	}
	for i, ev := range events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d: seq %d", i, ev.Seq)
		}
	}
}
