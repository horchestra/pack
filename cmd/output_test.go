package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/horchestra/pack/internal/oci"
	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target"
)

func TestClassify(t *testing.T) {
	missing := &render.Problems{Problems: []render.Problem{{Kind: render.KindRequired, Where: "/a", Message: "missing"}}}
	wrong := &render.Problems{Problems: []render.Problem{{Kind: render.KindRequired, Where: "/a"}, {Kind: render.KindRule, Where: "/b"}}}
	for err, want := range map[error]string{
		fmt.Errorf("render: %w", missing):  api.CodeValuesMissing,
		wrong:                              api.CodeValuesInvalid,
		fmt.Errorf("x: %w", oci.ErrNoLock): api.CodeNotRefreshed,
		fmt.Errorf("target x: %w", target.ErrNotBrought): api.CodeNotRefreshed,
		fmt.Errorf("%w: write pack:", release.ErrNoPack): api.CodeNoRelease,
		withCode(api.CodeSagaFailed, errors.New("boom")): api.CodeSagaFailed,
		errors.New("anything"):                           api.CodeError,
	} {
		if got := classify(err); got.Code != want {
			t.Errorf("%v: %s, want %s", err, got.Code, want)
		}
	}
	if e := classify(missing); len(e.Problems) != 1 || e.Problems[0].Where != "/a" {
		t.Errorf("problems: %+v", e)
	}
	ps := []render.Problem{{Kind: render.KindCheck, Where: "/x", Message: "no"}}
	if e := classify(withProblems(api.CodeCheckFailed, errors.New("1 problem(s)"), ps)); e.Code != api.CodeCheckFailed || len(e.Problems) != 1 {
		t.Errorf("a check's problems in its error: %+v", e)
	}
	for _, code := range []string{api.CodeNoRelease, api.CodeNotRefreshed, api.CodeValuesMissing, api.CodeSagaFailed} {
		if len(nextFor(code)) == 0 {
			t.Errorf("%s: nothing to run next", code)
		}
	}
}

// --json: stdout is the one document, whatever the command said along the way.
func TestJSONOutput(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	t.Cleanup(func() { jsonOut = false })
	var p plan.Plan
	out := pack(t, d, "plan", "--json")
	jsonOut = false
	if err := json.Unmarshal([]byte(out), &p); err != nil || p.Direction != "install" || len(p.Steps) == 0 {
		t.Fatalf("plan --json: %v\n%s", err, out)
	}
	out = pack(t, d, "apply", "--json")
	jsonOut = false
	var r installedResult
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Installed.Version != "2.1.0" || len(r.Installation) == 0 || r.Plan == nil {
		t.Fatalf("apply --json (the saga's events not in it): %v\n%s", err, out)
	}
	out = pack(t, d, "check", "--json")
	jsonOut = false
	var c struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(out), &c); err != nil || !c.OK {
		t.Fatalf("check --json: %v\n%s", err, out)
	}

	templateKey, templateOut = "", ""
	out = pack(t, d, "template", "--json")
	jsonOut = false
	var tf struct {
		Files []templateFile `json:"files"`
	}
	err := json.Unmarshal([]byte(out), &tf)
	if err != nil || !slices.ContainsFunc(tf.Files, func(f templateFile) bool { return f.Key == "openbao" && strings.Contains(f.Content, "api_addr") }) {
		t.Errorf("template --json: %v\n%s", err, out)
	}
	out = pack(t, d, "graph", "--json")
	jsonOut = false
	var g struct {
		Nodes []struct {
			Name      string   `json:"name"`
			Savepoint bool     `json:"savepoint"`
			Waits     []string `json:"waits"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(out), &g); err != nil || len(g.Nodes) < 2 || g.Nodes[0].Name != "postgres" || g.Nodes[len(g.Nodes)-1].Waits == nil {
		t.Errorf("graph --json: %v\n%s", err, out)
	}
	out = pack(t, d, "log", "--list", "--json")
	jsonOut = false
	_ = logCmd.Flags().Set("list", "false")
	var l struct {
		Runs []string `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &l); err != nil || len(l.Runs) == 0 {
		t.Errorf("log --list --json: %v\n%s", err, out)
	}
}
