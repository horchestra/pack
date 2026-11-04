package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/internal/testpacks/openbao"
	"github.com/horchestra/pack/sdk"
)

var packBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pack-mcp-")
	if err != nil {
		panic(err)
	}
	packBin = filepath.Join(dir, "pack")
	if out, err := exec.Command("go", "build", "-o", packBin, "../../cmd/pack").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func write(t *testing.T, dir, name string, v any) {
	t.Helper()
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// session is a client of the server of a release.
type session struct {
	t  *testing.T
	cs *mcp.ClientSession
}

func connect(t *testing.T, dir string) *session {
	t.Helper()
	ctx := context.Background()
	srv := New(&Server{Pack: packBin, Dir: dir, Env: append(os.Environ(), "PACK_ENV=")}, "test")
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &session{t, cs}
}

// call calls a tool and gives its answer: whether pack succeeded, and its --json document.
func (s *session) call(name string, args map[string]any) (bool, map[string]any) {
	s.t.Helper()
	r, err := s.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		s.t.Fatalf("%s: %v", name, err)
	}
	var out result
	if err := json.Unmarshal([]byte(r.Content[0].(*mcp.TextContent).Text), &out); err != nil {
		s.t.Fatalf("%s: %v", name, err)
	}
	var doc map[string]any
	_ = json.Unmarshal(out.Result, &doc)
	if r.IsError == out.OK {
		s.t.Errorf("%s: isError %v, ok %v", name, r.IsError, out.OK)
	}
	return out.OK, doc
}

func (s *session) state() string {
	s.t.Helper()
	_, doc := s.call("next", nil)
	st, _ := doc["state"].(string)
	return st
}

// A model takes a release from unfilled to healthy with the tools alone, then through a failure: diagnose, roll
// back. What is a person's — an approval — no tool gives.
func TestOperator(t *testing.T) {
	p := filepath.Join(t.TempDir(), "packs")
	if err := sdk.Generate(p, &openbao.OpenBao{}); err != nil {
		t.Fatal(err)
	}
	d := t.TempDir()
	write(t, d, "release.yaml", map[string]any{"pack": filepath.Join(p, "openbao")})
	env := map[string]any{"target": "memory", // no observe: what it holds is a process's, and each tool is a process of pack

		"keys": map[string]any{"postgres": map[string]any{"resources": map[string]any{"requests": map[string]any{"memory": "2Gi"}}}}}
	write(t, d, "env.yaml", env)
	write(t, d, "values.yaml", map[string]any{"postgres": map[string]any{"id": "n1"}})
	s := connect(t, d)

	tools, err := s.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
		if strings.Contains(tl.Name, "approve") || strings.Contains(string(mustJSON(tl.InputSchema)), "approve") {
			t.Errorf("%s: a person's approval in a tool", tl.Name)
		}
	}
	if len(names) != 16 {
		t.Errorf("the tools: %v", names)
	}

	if ok, doc := s.call("author_ref", map[string]any{"name": "action", "lang": "python"}); !ok || !strings.Contains(string(mustJSON(doc)), "pack run") {
		t.Errorf("author_ref: %v", doc)
	}

	if st := s.state(); st != "unfilled" {
		t.Fatalf("first: %s", st)
	}
	_, qs := s.call("questions", nil)
	if !strings.Contains(string(mustJSON(qs)), "/openbao/api_addr") {
		t.Fatalf("questions: %v", qs)
	}
	if ok, doc := s.call("answer", map[string]any{"answers": map[string]any{"/openbao/api_addr": "https://bao:8200"}}); !ok {
		t.Fatalf("answer: %v", doc)
	}
	if b, _ := os.ReadFile(filepath.Join(d, "values.yaml")); !strings.Contains(string(b), "https://bao:8200") || !strings.Contains(string(b), "llm") {
		t.Errorf("values.yaml:\n%s", b)
	}
	if st := s.state(); st != "planned" {
		t.Fatalf("answered: %s", st)
	}
	if ok, doc := s.call("plan", nil); !ok || doc["digest"] == nil {
		t.Errorf("plan: %v", doc)
	}
	if ok, doc := s.call("apply", nil); !ok {
		t.Fatalf("apply: %v", doc)
	}
	if st := s.state(); st != "applied" {
		t.Fatalf("applied: %s", st)
	}
	if ok, doc := s.call("status", nil); !ok || doc["healthy"] != true {
		t.Errorf("status: %v", doc)
	}
	if ok, doc := s.call("run", nil); !ok || !strings.Contains(string(mustJSON(doc)), "backup") {
		t.Errorf("run, the list: %v", doc)
	}

	// a failure: the saga fails, next says so, diagnose says why, rollback takes it back
	env["fail_at"], env["fail_error"] = "openbao/apply", "bind: address already in use"
	write(t, d, "env.yaml", env)
	write(t, d, "values.yaml", map[string]any{"postgres": map[string]any{"id": "n1"}, "openbao": map[string]any{"api_addr": "https://bao:8201"}})
	ok, doc := s.call("apply", nil)
	if e, _ := doc["error"].(map[string]any); ok || e["code"] != "saga_failed" {
		t.Fatalf("apply that fails: %v", doc)
	}
	if st := s.state(); st != "failed" {
		t.Errorf("failed: %s", st)
	}
	if ok, doc := s.call("diagnose", nil); !ok || doc["class"] != "port_conflict" {
		t.Errorf("diagnose: %v", doc)
	}
	delete(env, "fail_at")
	write(t, d, "env.yaml", env)
	if ok, doc := s.call("rollback", nil); !ok {
		t.Errorf("rollback: %v", doc)
	}

	// what a person approves stays theirs
	env["run"] = map[string]any{"approve": "plan"}
	write(t, d, "env.yaml", env)
	ok, doc = s.call("apply", nil)
	if e, _ := doc["error"].(map[string]any); ok || e["code"] != "needs_approval" {
		t.Errorf("apply under run.approve: plan: %v", doc)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// A pack that fails saying nothing gives no log; the zero Server's pack has this process's environment.
func TestCallSaysWhatWasSaid(t *testing.T) {
	sh := filepath.Join(t.TempDir(), "pack")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\ntest -n \"$HOME\" || { echo no home >&2; exit 1; }\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, _, err := (&Server{Pack: sh, Dir: t.TempDir()}).call(context.Background(), "next")
	if err != nil {
		t.Fatal(err)
	}
	var out result
	if err := json.Unmarshal([]byte(r.Content[0].(*mcp.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if out.OK || !r.IsError || out.Log != nil {
		t.Errorf("a silent failure: %+v", out)
	}
}
