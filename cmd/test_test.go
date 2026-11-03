package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scenarios of the examples hold: postgres alone, openbao with the postgres it holds.
func TestExampleScenarios(t *testing.T) {
	for _, ex := range []string{"postgres", "openbao"} {
		root := filepath.Join(examples, ex)
		out := pack(t, root, "test", filepath.Join(root, "golden", ex), filepath.Join(root, "tests"))
		if !strings.Contains(out, "scenario(s) ok") {
			t.Errorf("%s:\n%s", ex, out)
		}
	}
}

// A scenario of the test pack: a suggestion from the facts, a failure after the savepoint keeping what is before it,
// the pack's own diagnosis with the fix worked out; and one that does not hold says why.
func TestScenario(t *testing.T) {
	packDir := generated(t)
	d := t.TempDir()
	scenario := func(name, body string) string {
		p := filepath.Join(d, name+".yaml")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const values = `values:
  openbao:
    api_addr: https://bao:8200
  postgres:
    id: n1
env:
  keys:
    postgres:
      resources:
        requests:
          memory: 2Gi
`
	ok := scenario("port", `name: 5433 taken on a host
facts:
  default:
    at: 2026-11-03T00:00:00Z
    hosts:
      db1:
        ports:
          - port: 5433
`+values+`fail_at: openbao/apply
fail_error: "bind: address already in use :5433"
logs:
  postgres:
    postgres-0:
      - "could not bind 0.0.0.0:5433: address already in use"
expect:
  suggest:
    /postgres/pg_port: 5434
  problems: []
  apply:
    state: failed
    in_effect:
      - postgres
`)
	r := runScenario(context.Background(), must(memoryStand(packDir)), ok)
	if !r.OK {
		t.Fatalf("%s: %v", r.Name, r.Failures)
	}

	diag := scenario("diag", values+`fail_at: postgres/wait
fail_error: "could not bind 0.0.0.0:5433: address already in use"
expect:
  diagnose:
    class: port-taken
    diagnosis: port-taken
    remedies:
      - patch: /postgres/pg_port
        value: 5434
`)
	if r := runScenario(context.Background(), must(memoryStand(packDir)), diag); !r.OK {
		t.Errorf("diagnose: %v", r.Failures)
	}

	bad := scenario("bad", values+`expect:
  suggest:
    /postgres/pg_port: 5434
  problems:
    - where: /openbao/api_addr
  apply:
    state: failed
`)
	r = runScenario(context.Background(), must(memoryStand(packDir)), bad)
	got := strings.Join(r.Failures, "\n")
	for _, want := range []string{"suggest /postgres/pg_port: nothing offered, 5434 expected", "problems: none at /openbao/api_addr", "apply: installed"} {
		if !strings.Contains(got, want) {
			t.Errorf("a scenario that does not hold says %q:\n%s", want, got)
		}
	}
	if r.OK {
		t.Error("ok")
	}

	// a render that fails with files expected is a failure, not a pass that compared nothing
	broken := scenario("broken", `values:
  openbao:
    api_addr: 42
expect:
  problems:
    - where: /openbao/api_addr
  files: golden/none
`)
	if r := runScenario(context.Background(), must(memoryStand(packDir)), broken); r.OK || !strings.Contains(strings.Join(r.Failures, "\n"), "the values do not render") {
		t.Errorf("files of values that do not render: %v", r.Failures)
	}
}

// --update writes the golden files; a file or a plan that differs from them is a failure that says where.
func TestScenarioGolden(t *testing.T) {
	packDir := generated(t)
	d := t.TempDir()
	sc := filepath.Join(d, "golden.yaml")
	if err := os.WriteFile(sc, []byte(`values:
  openbao:
    api_addr: https://bao:8200
  postgres:
    id: n1
env:
  keys:
    postgres:
      resources:
        requests:
          memory: 2Gi
from:
  values:
    openbao:
      api_addr: https://bao:8201
    postgres:
      id: n1
expect:
  files: golden/files
  plan: golden/plan.txt
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testUpdate = false })
	testUpdate = true
	if r := runScenario(context.Background(), must(memoryStand(packDir)), sc); !r.OK {
		t.Fatalf("--update: %v", r.Failures)
	}
	testUpdate = false
	if r := runScenario(context.Background(), must(memoryStand(packDir)), sc); !r.OK {
		t.Fatalf("against what --update wrote: %v", r.Failures)
	}
	plan := filepath.Join(d, "golden", "plan.txt")
	if err := os.WriteFile(plan, []byte("another plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var file string
	_ = filepath.WalkDir(filepath.Join(d, "golden", "files"), func(p string, e os.DirEntry, _ error) error {
		if !e.IsDir() && len(file) == 0 {
			file = p
		}
		return nil
	})
	if err := os.WriteFile(file, []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "golden", "files", "extra"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runScenario(context.Background(), must(memoryStand(packDir)), sc)
	got := strings.Join(r.Failures, "\n")
	for _, want := range []string{"plan differs from " + plan, "line 1:", "differs from " + file, `want "edited"`, "extra is not rendered"} {
		if !strings.Contains(got, want) {
			t.Errorf("the failures say %q:\n%s", want, got)
		}
	}

	empty := t.TempDir()
	if _, err := scenarioFiles([]string{sc, empty}); err == nil || !strings.Contains(err.Error(), "no *.yaml") {
		t.Errorf("an empty directory of scenarios: %v", err)
	}
}
