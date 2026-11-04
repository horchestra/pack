package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/horchestra/pack/pkg/api"
)

// The --json document of each of these commands parses into its type of pkg/api: what an operator or a UI reads.
func TestJSONDocumentsAreAPITypes(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	t.Cleanup(func() { jsonOut = false })
	doc := func(args ...string) string {
		t.Helper()
		out := pack(t, d, append(args, "--json")...)
		jsonOut = false
		return out
	}

	var vr api.VersionResult
	if err := json.Unmarshal([]byte(doc("version")), &vr); err != nil || len(vr.Version) == 0 || vr.Formats["json"] != api.FormatJSON || vr.Formats["stream"] != api.FormatStream {
		t.Fatalf("version --json: %v, %+v", err, vr)
	}

	var ar api.InstalledResult
	if err := json.Unmarshal([]byte(doc("apply")), &ar); err != nil || ar.Installed.Version != "2.1.0" || ar.Plan == nil {
		t.Fatalf("apply --json: %v, %+v", err, ar)
	}

	var st api.Status
	if err := json.Unmarshal([]byte(doc("status")), &st); err != nil || st.State != "installed" || !st.Healthy {
		t.Fatalf("status --json: %v, %+v", err, st)
	}

	var hist []api.Revision
	out := doc("history")
	if err := json.Unmarshal([]byte(out), &hist); err != nil || len(hist) == 0 || !hist[len(hist)-1].Current {
		t.Fatalf("history --json: %v, %+v", err, hist)
	}

	var acts api.ActionsResult
	if err := json.Unmarshal([]byte(doc("run")), &acts); err != nil {
		t.Fatalf("run --json: %v, %+v", err, acts)
	}

	var lr api.LogResult
	if err := json.Unmarshal([]byte(doc("log")), &lr); err != nil || len(lr.Saga) == 0 || len(lr.Entries) == 0 {
		t.Fatalf("log --json: %v, %+v", err, lr)
	}

	var runs api.LogRunsResult
	out = doc("log", "--list")
	_ = logCmd.Flags().Set("list", "false")
	if err := json.Unmarshal([]byte(out), &runs); err != nil || len(runs.Runs) == 0 {
		t.Fatalf("log --list --json: %v, %+v", err, runs)
	}

	var tr api.TemplateResult
	if err := json.Unmarshal([]byte(doc("template")), &tr); err != nil || len(tr.Files) == 0 {
		t.Fatalf("template --json: %v, %+v", err, tr)
	}

	var gr api.GraphResult
	if err := json.Unmarshal([]byte(doc("graph")), &gr); err != nil || len(gr.Nodes) < 2 {
		t.Fatalf("graph --json: %v, %+v", err, gr)
	}

	var cr api.CheckResult
	if err := json.Unmarshal([]byte(doc("check")), &cr); err != nil || !cr.OK {
		t.Fatalf("check --json: %v, %+v", err, cr)
	}

	var dr api.DiscoverResult
	if err := json.Unmarshal([]byte(doc("discover")), &dr); err != nil {
		t.Fatalf("discover --json: %v, %+v", err, dr)
	}

	var ex api.Explanation
	if err := json.Unmarshal([]byte(doc("values", "explain", "/postgres/id")), &ex); err != nil || ex.Pointer != "/postgres/id" || !ex.Given {
		t.Fatalf("values explain --json: %v, %+v", err, ex)
	}

	var rr api.RemovedResult
	out = doc("delete", "--dry-run")
	deleteDry = false // a flag keeps its value between runs in one process
	if err := json.Unmarshal([]byte(out), &rr); err != nil || len(rr.Keys) == 0 || len(rr.Digest) == 0 || !rr.DryRun {
		t.Fatalf("delete --dry-run --json: %v, %+v", err, rr)
	}
}

// pack version says the binary's version; with --json, the versions of its documents with it.
func TestVersion(t *testing.T) {
	out := pack(t, t.TempDir(), "version")
	if !strings.HasPrefix(out, "pack ") {
		t.Fatalf("version: %q", out)
	}
	if out := pack(t, t.TempDir(), "--version"); !strings.HasPrefix(out, "pack version ") {
		t.Fatalf("--version: %q", out)
	}
}
