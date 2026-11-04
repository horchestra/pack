package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"

	"github.com/horchestra/pack-operator/config"
	opgit "github.com/horchestra/pack-operator/git"
	"github.com/horchestra/pack-operator/server"
	"github.com/horchestra/pack-operator/worker"
)

// kv is the fixture pack: one setting, no hooks — the smallest pack the sdk writes.
type kv struct {
	Value string `json:"value"`
}

func (*kv) SetDefaults()              {}
func (*kv) Meta() sdk.Meta            { return sdk.Meta{Name: "kv", Version: "1.0.0"} }
func (*kv) PackRoot() packschema.Root { return packschema.Root{} }

var packBin = sync.OnceValues(func() (string, error) {
	bin := filepath.Join(os.TempDir(), "pack-mcp-test")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/pack")
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%s: %w", out, err)
	}
	return bin, nil
})

// rig is a running operator over a fixture repository, with a native worker doing the jobs, and an MCP
// client of its API.
type rig struct {
	t    *testing.T
	cs   *mcp.ClientSession
	base string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	bin, err := packBin()
	if err != nil {
		t.Fatalf("the pack binary: %v", err)
	}
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
		"pack: ../../../packs/kv\nstate: db\nid: 11111111-2222-3333-4444-555555555555\n")
	write("releases/prod/db/values.yaml", "value: one\n")
	write("releases/prod/db/env.yaml", `{"target": "memory", "run": {"approve": "plan"}}`+"\n")
	wt, err := seed.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("the start", &git.CommitOptions{Author: &object.Signature{Name: "alice", Email: "alice@example.org"}}); err != nil {
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
		DB:               "sqlite://" + filepath.Join(t.TempDir(), "operator.db"),
		Auth:             "none",
		DevUser:          "dev",
		Pack:             bin,
		DataDir:          dataDir,
		StateDSN:         "sqlite://" + filepath.Join(t.TempDir(), "state.db"),
		EncryptionKeyEnv: "PACK_OPERATOR_TEST_KEY",
		Git:              config.Git{FetchInterval: config.Duration{Duration: time.Hour}},
		Projects:         []config.Project{{ID: "dev", Repo: "file://" + bare, Branch: "main", Runtime: "native"}},
	}
	t.Setenv("PACK_OPERATOR_TEST_KEY", "test-key")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv, err := server.NewServer(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	go func() { _ = srv.Serve(ctx, ln) }()
	base := "http://" + ln.Addr().String()
	w := worker.New(cfg, srv.Bus(), srv.DB(), nil, opgit.NewRepos(filepath.Join(dataDir, "repos"), nil),
		"native", nil, worker.Native{Pack: bin, Log: func(line string) { t.Log(line) }})
	go func() { _ = w.Run(ctx) }()
	// the first sync, then the release is at HEAD
	post(t, base+"/api/sync", `{"repo":"dev","ref":"main"}`)
	deadline := time.Now().Add(30 * time.Second)
	synced := false
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/api/projects/dev/releases")
		if err == nil && resp.StatusCode == http.StatusOK {
			var rels []struct {
				SHA string `json:"sha"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&rels)
			resp.Body.Close()
			if len(rels) == 1 && len(rels[0].SHA) > 0 {
				synced = true
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !synced {
		t.Fatal("the sync never showed the release")
	}
	st, ct := mcp.NewInMemoryTransports()
	msrv := New(&Client{Base: base}, "test")
	if _, err := msrv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &rig{t, cs, base}
}

func post(t *testing.T, url, body string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

// call calls a tool and gives its answer's document.
func (r *rig) call(name string, args map[string]any) (bool, json.RawMessage) {
	r.t.Helper()
	res, err := r.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		r.t.Fatalf("%s: %v", name, err)
	}
	var out result
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &out); err != nil {
		r.t.Fatalf("%s: %v", name, err)
	}
	return out.OK, out.Result
}

const release = "dev:releases/prod/db"

// The tools of К10: the list is whole, and nothing in it approves — that is a person's.
func TestTools(t *testing.T) {
	r := newRig(t)
	tools, err := r.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"next", "check", "plan", "status", "history", "logs", "journal", "request", "job"}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
		if strings.Contains(tl.Name, "approve") {
			t.Errorf("%s: a person's approval in a tool", tl.Name)
		}
	}
	for _, w := range want {
		found := false
		for _, n := range names {
			found = found || n == w
		}
		if !found {
			t.Errorf("no tool %s in %v", w, names)
		}
	}
	if len(names) != len(want) {
		t.Errorf("the tools: %v", names)
	}
}

// next on the fixture's release: nothing installed, the values given — planned. A path with no release is new.
func TestNext(t *testing.T) {
	r := newRig(t)
	deadline := time.Now().Add(30 * time.Second)
	var state string
	for time.Now().Before(deadline) {
		ok, doc := r.call("next", map[string]any{"release": release})
		if ok {
			var n struct {
				State string `json:"state"`
			}
			_ = json.Unmarshal(doc, &n)
			state = n.State
			if state == "planned" {
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if state != "planned" {
		t.Fatalf("next of a filled, never applied release: %s", state)
	}
	ok, doc := r.call("next", map[string]any{"release": "dev:releases/nowhere"})
	var n struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(doc, &n)
	if !ok || n.State != "new" {
		t.Errorf("next of no release: %v %s", ok, doc)
	}
}

// plan walks the release to needs_approval: the request waits, the digest is there, and no tool approves.
// A person approves through the API; then the release comes applied, and status, history, journal and job
// read what happened.
func TestPlanToApproval(t *testing.T) {
	r := newRig(t)
	ok, doc := r.call("plan", map[string]any{"release": release})
	var page struct {
		ID     string `json:"id"`
		State  string `json:"state"`
		Digest string `json:"digest"`
		SHA    string `json:"sha"`
	}
	_ = json.Unmarshal(doc, &page)
	if !ok || page.State != "waiting_approval" {
		t.Fatalf("plan: %v %s", ok, doc)
	}
	if len(page.Digest) == 0 || len(page.SHA) == 0 {
		t.Fatalf("the plan waits on nothing: %s", doc)
	}
	// a person approves — through the API, never through a tool
	post(t, r.base+"/api/requests/"+page.ID+"/approve",
		fmt.Sprintf(`{"digest": %q, "sha": %q}`, page.Digest, page.SHA))
	// the approved request applies; the release comes applied
	deadline := time.Now().Add(90 * time.Second)
	applied := false
	for time.Now().Before(deadline) {
		ok, doc := r.call("next", map[string]any{"release": release})
		var n struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(doc, &n)
		if ok && n.State == "applied" {
			applied = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !applied {
		t.Fatal("the approved plan never applied")
	}
	if ok, doc := r.call("status", map[string]any{"release": release}); !ok {
		t.Errorf("status: %s", doc)
	}
	if ok, doc := r.call("history", map[string]any{"release": release}); !ok {
		t.Errorf("history: %s", doc)
	}
	ok, doc = r.call("journal", map[string]any{"release": release})
	var sagas []struct {
		ID      string `json:"id"`
		Outcome string `json:"outcome"`
	}
	_ = json.Unmarshal(doc, &sagas)
	if !ok || len(sagas) == 0 || sagas[0].Outcome != "done" {
		t.Fatalf("journal, the sagas: %v %s", ok, doc)
	}
	ok, doc = r.call("journal", map[string]any{"release": release, "saga": sagas[0].ID})
	var events []json.RawMessage
	_ = json.Unmarshal(doc, &events)
	if !ok || len(events) == 0 {
		t.Fatalf("journal of %s: %v %s", sagas[0].ID, ok, doc)
	}
	// a request and its job
	ok, doc = r.call("request", map[string]any{"release": release, "action": "refresh"})
	var req struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(doc, &req)
	if !ok || len(req.ID) == 0 {
		t.Fatalf("request refresh: %v %s", ok, doc)
	}
	// the refresh runs to its end; the job tool says it
	deadline = time.Now().Add(60 * time.Second)
	var jobID string
	for time.Now().Before(deadline) {
		resp, err := http.Get(r.base + "/api/requests/" + req.ID)
		if err == nil && resp.StatusCode == http.StatusOK {
			var full struct {
				State string `json:"state"`
				Jobs  []struct {
					ID string `json:"id"`
				} `json:"jobs"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&full)
			resp.Body.Close()
			if (full.State == "done" || full.State == "failed") && len(full.Jobs) > 0 {
				if full.State != "done" {
					t.Fatalf("the refresh: %s", full.State)
				}
				jobID = full.Jobs[len(full.Jobs)-1].ID
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(jobID) == 0 {
		t.Fatal("the refresh never ran")
	}
	ok, doc = r.call("job", map[string]any{"id": jobID})
	var out struct {
		Job struct {
			State string `json:"state"`
		} `json:"job"`
		Events []json.RawMessage `json:"events"`
	}
	_ = json.Unmarshal(doc, &out)
	if !ok || out.Job.State != "succeeded" || len(out.Events) == 0 {
		t.Errorf("job %s: %v %s", jobID, ok, doc)
	}
}
