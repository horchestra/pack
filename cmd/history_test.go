package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/workdir"
)

func installedValues(t *testing.T, d string) map[string]any {
	t.Helper()
	rel, err := release.Load(d, "")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := workdir.New(rel, "").FileState().Load()
	if err != nil || rec == nil {
		t.Fatalf("record: %v %v", rec, err)
	}
	var v struct {
		Values map[string]any `json:"values"`
	}
	if err := json.Unmarshal(rec.Values, &v); err != nil {
		t.Fatal(err)
	}
	return v.Values
}

// current: the line of the table is revision n, marked the one installed.
func current(line, n string) bool {
	f := strings.Fields(line)
	return len(f) > 1 && f[0] == "*" && f[1] == n
}

// Every saga that ends is a revision; rollback --to goes back to one with its values, as a saga of its own.
func TestHistoryAndRollbackTo(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	historyJSON, rollbackTo, rollbackDry = false, 0, false
	t.Cleanup(func() { historyJSON, rollbackTo, rollbackDry = false, 0, false })
	pack(t, d, "apply")
	write(t, d, "values.yaml", map[string]any{
		"openbao":  map[string]any{"api_addr": "https://bao:8300"},
		"postgres": map[string]any{"id": "n1"},
	})
	pack(t, d, "apply")
	out := pack(t, d, "history")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 3 || !current(lines[2], "2") || current(lines[1], "1") ||
		!strings.Contains(lines[1], "installed") {
		t.Fatalf("history:\n%s", out)
	}
	if got := installedValues(t, d)["openbao"].(map[string]any)["api_addr"]; got != "https://bao:8300" {
		t.Fatalf("installed %v", got)
	}

	out = pack(t, d, "rollback", "--to", "1")
	rollbackTo = 0
	if !strings.Contains(out, "back to revision 1") {
		t.Fatalf("rollback --to 1:\n%s", out)
	}
	if got := installedValues(t, d)["openbao"].(map[string]any)["api_addr"]; got != "https://bao:8200" {
		t.Errorf("after rollback --to 1: %v", got)
	}
	out = pack(t, d, "history")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 4 || !current(lines[3], "3") {
		t.Errorf("a rollback is a revision of its own:\n%s", out)
	}
	if out := pack(t, d, "rollback", "--to", "3"); !strings.Contains(out, "revision 3 is what is installed") {
		t.Errorf("to the current one: %s", out)
	}
	if out := pack(t, d, "rollback", "--to", "1"); !strings.Contains(out, "revision 1 is the same as what is installed") {
		t.Errorf("to one the same as the current one: %s", out)
	}
	rollbackTo = 0
	out = pack(t, d, "history", "--json")
	var revs []map[string]any
	if err := json.Unmarshal([]byte(out), &revs); err != nil || len(revs) != 3 || revs[0]["values"] == nil || revs[2]["current"] != true || revs[0]["current"] != nil {
		t.Errorf("--json: %v\n%s", err, out)
	}

	// taken away, the history stays, and a revision is installed again from it
	deleteDry = false
	pack(t, d, "delete")
	out = pack(t, d, "history")
	if !strings.Contains(out, "removed") || strings.Contains(out, "*") {
		t.Errorf("after delete:\n%s", out)
	}
	out = pack(t, d, "rollback", "--to", "2")
	rollbackTo = 0
	if !strings.Contains(out, "back to revision 2") {
		t.Fatalf("rollback --to 2 after delete:\n%s", out)
	}
	if got := installedValues(t, d)["openbao"].(map[string]any)["api_addr"]; got != "https://bao:8300" {
		t.Errorf("after rollback --to 2: %v", got)
	}
}
