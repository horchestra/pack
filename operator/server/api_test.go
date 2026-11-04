package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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

	"github.com/horchestra/pack-operator/bus"
	"github.com/horchestra/pack-operator/config"
	opgit "github.com/horchestra/pack-operator/git"
	"github.com/horchestra/pack-operator/worker"
)


// testRepos is the worker's working copies of a data dir.
func testRepos(t *testing.T, dataDir string) *opgit.Repos {
	t.Helper()
	return opgit.NewRepos(filepath.Join(dataDir, "repos"), nil)
}

// kv is the fixture pack: one setting, no hooks — the smallest pack the sdk writes.
type kv struct {
	Value string `json:"value"`
}

func (*kv) SetDefaults()              {}
func (*kv) Meta() sdk.Meta            { return sdk.Meta{Name: "kv", Version: "1.0.0"} }
func (*kv) PackRoot() packschema.Root { return packschema.Root{} }

// packBin is the pack binary, built once for the package's tests.
var packBin = sync.OnceValues(func() (string, error) {
	bin := filepath.Join(os.TempDir(), "pack-api-test")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/pack")
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%s: %w", out, err)
	}
	return bin, nil
})

// apiRig is a running operator over a fixture repository, with a native worker doing the jobs.
type apiRig struct {
	t   *testing.T
	url string
	cfg *config.Config
	bus *bus.SQLBus
	head string
}

func newAPIRig(t *testing.T) *apiRig {
	t.Helper()
	return newAPIRigWith(t, "pack: ../../../packs/kv\n")
}

// newAPIRigWith is newAPIRig with the release's own head of release.yaml (B10's fixture names a pack in a registry).
func newAPIRigWith(t *testing.T, releaseHead string) *apiRig {
	t.Helper()
	bin, err := packBin()
	if err != nil {
		t.Fatalf("the pack binary: %v", err)
	}
	// the fixture: packs/kv, releases/prod/db on the memory target, a bare remote
	root := t.TempDir()
	bare := filepath.Join(root, "repo.git")
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
		releaseHead+"state: db\nid: 11111111-2222-3333-4444-555555555555\n")
	write("releases/prod/db/values.yaml", "value: one\n")
	write("releases/prod/db/env.yaml", `{"target": "memory", "run": {"approve": "plan"}}`+"\n")
	wt, err := seed.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	h, err := wt.Commit("the start", &git.CommitOptions{Author: &object.Signature{Name: "alice", Email: "alice@example.org"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.CreateRemote(&ggconfig.RemoteConfig{Name: "origin", URLs: []string{bare}}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Push(&git.PushOptions{RefSpecs: []ggconfig.RefSpec{"refs/heads/master:refs/heads/main"}}); err != nil {
		t.Fatal(err)
	}
	b, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	cfg := &config.Config{
		DB:       "sqlite://" + filepath.Join(t.TempDir(), "operator.db"),
		Auth:     "none",
		DevUser:  "dev",
		Pack:     bin,
		DataDir:  dataDir,
		StateDSN: "sqlite://" + filepath.Join(t.TempDir(), "state.db"),
		EncryptionKeyEnv: "PACK_OPERATOR_TEST_KEY",
		Git:      config.Git{FetchInterval: config.Duration{Duration: time.Hour}}, // the tests sync by hand
		Projects: []config.Project{{ID: "dev", Repo: "file://" + bare, Branch: "main", Runtime: "native"}},
	}
	t.Setenv("PACK_OPERATOR_TEST_KEY", "test-key")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv, err := NewServer(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	go func() { _ = srv.Serve(ctx, ln) }()
	rig := &apiRig{t: t, url: "http://" + ln.Addr().String(), cfg: cfg, head: h.String()}
	// the worker does the jobs the requests queue — the server's own Bus, a working copy of its own
	rig.bus = srv.Bus()
	w := worker.New(cfg, srv.bus, srv.db, nil, testRepos(t, dataDir), "native", nil, worker.Native{Pack: bin, Log: func(line string) { t.Log(line) }})
	go func() { _ = w.Run(ctx) }()
	// the first sync, then everything is of it
	rig.post(t, "/api/sync", `{"repo":"dev","ref":"main"}`, http.StatusAccepted)
	rig.waitReleases(t)
	return rig
}

// waitReleases waits until the sync gives the project's releases a HEAD.
func (r *apiRig) waitReleases(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var rels []struct {
			Path string `json:"path"`
			SHA  string `json:"sha"`
		}
		if r.get(t, "/api/projects/dev/releases", &rels) && len(rels) == 1 && rels[0].SHA == r.head {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("the sync never showed the release")
}

// waitRequest polls the request until it is in one of the states, and answers it whole.
func (r *apiRig) waitRequest(t *testing.T, id string, states ...string) bus.Request {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var req bus.Request
	for time.Now().Before(deadline) {
		if r.get(t, "/api/requests/"+id, &req) {
			for _, s := range states {
				if req.State == s {
					return req
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("the request %s: %s, waiting for %s", id, req.State, states)
	return req
}

// approve approves the request's waiting plan through the API (run.approve: plan of the fixture).
func (r *apiRig) approve(t *testing.T, id string) {
	t.Helper()
	r.waitRequest(t, id, bus.RequestWaitingApproval)
	var page struct {
		SHA    string `json:"sha"`
		Digest string `json:"digest"`
	}
	if !r.get(t, "/api/requests/"+id, &page) || len(page.Digest) == 0 {
		t.Fatalf("the request %s waits on no digest", id)
	}
	body := fmt.Sprintf(`{"digest": %q, "sha": %q}`, page.Digest, page.SHA)
	resp := r.post(t, "/api/requests/"+id+"/approve", body, http.StatusOK)
	resp.Body.Close()
}

func (r *apiRig) get(t *testing.T, path string, out any) bool {
	t.Helper()
	resp, err := http.Get(r.url + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return json.NewDecoder(resp.Body).Decode(out) == nil
}

func (r *apiRig) post(t *testing.T, path, body string, want ...int) *http.Response {
	t.Helper()
	return r.do(t, "POST", path, body, "", want...)
}

func (r *apiRig) do(t *testing.T, method, path, body, user string, want ...int) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, r.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(user) > 0 {
		req.Header.Set("X-Pack-User", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) > 0 {
		ok := false
		for _, code := range want {
			ok = ok || resp.StatusCode == code
		}
		if !ok {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("%s %s: %d: %s", method, path, resp.StatusCode, b)
		}
	}
	return resp
}

// The version, the projects, the releases of the synced HEAD.
func TestAPIBasics(t *testing.T) {
	r := newAPIRig(t)
	var version struct {
		Operator string `json:"operator"`
	}
	if !r.get(t, "/api/version", &version) {
		t.Fatal("GET /api/version")
	}
	var projects []struct {
		ID string `json:"id"`
	}
	if !r.get(t, "/api/projects", &projects) || len(projects) != 1 || projects[0].ID != "dev" {
		t.Fatalf("GET /api/projects: %+v", projects)
	}
	var rel struct {
		Path    string `json:"path"`
		State   string `json:"state"`
		Waiting []any  `json:"waiting"`
	}
	if !r.get(t, "/api/releases/dev:releases%2Fprod%2Fdb", &rel) {
		t.Fatal("GET /api/releases/dev:releases/prod/db")
	}
	if rel.State != "new" {
		t.Errorf("a fresh release: %s", rel.State)
	}
}

// A synchronous operation: pack status --json on the checkout, no job queued. Every reading endpoint of К4
// answers on the fixture's release.
func TestAPISyncOp(t *testing.T) {
	r := newAPIRig(t)
	var status map[string]any
	if !r.get(t, "/api/releases/dev:releases%2Fprod%2Fdb/status", &status) {
		t.Fatal("GET status")
	}
	for _, op := range []string{"history", "logs", "actions", "check", "template", "graph", "schema", "explain"} {
		resp, err := http.Get(r.url + "/api/releases/dev:releases%2Fprod%2Fdb/" + op)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			t.Errorf("GET %s: %d: %s", op, resp.StatusCode, b)
		}
	}
}

// A request is idempotent; a foreign user may not ask; the jobs it queues end; the events come as SSE with
// Last-Event-ID honored.
func TestAPIRequestAndEvents(t *testing.T) {
	r := newAPIRig(t)
	resp := r.do(t, "POST", "/api/releases/dev:releases%2Fprod%2Fdb/requests",
		`{"action": "plan"}`, "mallory", http.StatusForbidden)
	resp.Body.Close()
	var req bus.Request
	resp = r.post(t, "/api/releases/dev:releases%2Fprod%2Fdb/requests", `{"action": "plan"}`, http.StatusOK, http.StatusCreated)
	if err := json.NewDecoder(resp.Body).Decode(&req); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// the same work asked again: the same request stands
	var again bus.Request
	resp = r.post(t, "/api/releases/dev:releases%2Fprod%2Fdb/requests", `{"action": "plan"}`, http.StatusOK, http.StatusCreated)
	if err := json.NewDecoder(resp.Body).Decode(&again); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if again.ID != req.ID {
		t.Errorf("the idempotence: %s and %s", req.ID, again.ID)
	}
	// run.approve: plan — the plan waits, the API's approval runs it
	r.approve(t, req.ID)
	// the request's page: the jobs it queued
	var full struct {
		State string    `json:"state"`
		Jobs  []bus.Job `json:"jobs"`
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if r.get(t, "/api/requests/"+req.ID, &full) && full.State == bus.RequestDone {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if full.State != bus.RequestDone {
		t.Fatalf("the request: %s", full.State)
	}
	if len(full.Jobs) == 0 {
		t.Fatal("no jobs of the request")
	}
	// the events as SSE, from the middle on (Last-Event-ID)
	jobID := full.Jobs[len(full.Jobs)-1].ID
	events := r.sse(t, "/api/jobs/"+jobID+"/events", 0)
	if len(events) == 0 {
		t.Fatal("no events of the job")
	}
	for i, id := range events {
		if id != int64(i+1) {
			t.Fatalf("the event ids: %v", events)
		}
	}
	from := events[len(events)/2]
	rest := r.sse(t, "/api/jobs/"+jobID+"/events", from)
	if len(rest) != len(events)-int(from) || rest[0] != from+1 {
		t.Fatalf("the reconnect from %d: %v of %v", from, rest, events)
	}
}

// sse reads an SSE stream to its end: the ids it carried.
func (r *apiRig) sse(t *testing.T, path string, lastID int64) []int64 {
	t.Helper()
	req, err := http.NewRequest("GET", r.url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lastID > 0 {
		req.Header.Set("Last-Event-ID", fmt.Sprint(lastID))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", path, resp.StatusCode)
	}
	var ids []int64
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if id, ok := strings.CutPrefix(line, "id: "); ok {
			var n int64
			if _, err := fmt.Sscan(id, &n); err == nil {
				ids = append(ids, n)
			}
		}
	}
	return ids
}

// An apply through the pipeline: the state store holds the record, the journal the saga; the API reads both.
func TestAPIApplyAndState(t *testing.T) {
	r := newAPIRig(t)
	var req bus.Request
	resp := r.post(t, "/api/releases/dev:releases%2Fprod%2Fdb/requests", `{"action": "apply"}`, http.StatusOK, http.StatusCreated)
	if err := json.NewDecoder(resp.Body).Decode(&req); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	r.approve(t, req.ID)
	deadline := time.Now().Add(90 * time.Second)
	var full struct {
		State string    `json:"state"`
		Jobs  []bus.Job `json:"jobs"`
	}
	for time.Now().Before(deadline) {
		if r.get(t, "/api/requests/"+req.ID, &full) && (full.State == bus.RequestDone || full.State == bus.RequestFailed) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if full.State != bus.RequestDone {
		for _, j := range full.Jobs {
			t.Logf("job %s %s argv=%v exit=%d result=%s", j.ID, j.State, j.Argv, j.Exit, j.Result)
		}
		var all []bus.Job
		if r.get(t, "/api/jobs", &all) {
			for _, j := range all {
				t.Logf("all: job %s %s argv=%v exit=%d", j.ID, j.State, j.Argv, j.Exit)
			}
		}
		t.Fatalf("the apply: %s", full.State)
	}
	var rel struct {
		State        string `json:"state"`
		InstalledSHA string `json:"installed_sha"`
	}
	if !r.get(t, "/api/releases/dev:releases%2Fprod%2Fdb", &rel) || rel.State != "applied" || rel.InstalledSHA != r.head {
		t.Fatalf("the release after the apply: %+v", rel)
	}
	var sagas []struct {
		ID      string `json:"id"`
		Outcome string `json:"outcome"`
	}
	if !r.get(t, "/api/releases/dev:releases%2Fprod%2Fdb/sagas", &sagas) || len(sagas) == 0 {
		t.Fatalf("the sagas: %+v", sagas)
	}
	if sagas[0].Outcome != "done" {
		t.Errorf("the saga's outcome: %s", sagas[0].Outcome)
	}
	// the saga's events as SSE, replayed
	events := r.sse(t, "/api/releases/dev:releases%2Fprod%2Fdb/sagas/"+sagas[0].ID+"/events", 0)
	if len(events) == 0 {
		t.Error("no events of the saga")
	}
	var facts struct {
		Facts map[string]json.RawMessage `json:"facts"`
	}
	if !r.get(t, "/api/releases/dev:releases%2Fprod%2Fdb/facts", &facts) {
		t.Error("GET facts")
	}
	var audit []json.RawMessage
	if !r.get(t, "/api/audit", &audit) || len(audit) == 0 {
		t.Error("GET /api/audit")
	}
}

// The secrets: created, listed without values; the projects: written by an admin, refused to a stranger.
func TestAPISecretsAndProjects(t *testing.T) {
	r := newAPIRig(t)
	resp := r.post(t, "/api/secrets", `{"name": "git/dev", "fields": {"token": "x", "username": "u"}}`, http.StatusCreated)
	resp.Body.Close()
	var secrets []struct {
		Name string   `json:"name"`
		Keys []string `json:"keys"`
	}
	if !r.get(t, "/api/secrets?prefix=git", &secrets) || len(secrets) != 1 {
		t.Fatalf("GET /api/secrets: %+v", secrets)
	}
	if secrets[0].Name != "git/dev" || len(secrets[0].Keys) != 2 {
		t.Errorf("the secret's keys: %+v", secrets[0])
	}
	resp = r.do(t, "PUT", "/api/projects",
		`{"id": "two", "repo": "file:///nowhere", "branch": "main"}`, "mallory", http.StatusForbidden)
	resp.Body.Close()
	resp = r.do(t, "PUT", "/api/projects",
		`{"id": "two", "repo": "file:///nowhere", "branch": "main"}`, "dev", http.StatusCreated)
	resp.Body.Close()
	var projects []struct {
		ID string `json:"id"`
	}
	if !r.get(t, "/api/projects", &projects) || len(projects) != 2 {
		t.Fatalf("the projects after PUT: %+v", projects)
	}
}
