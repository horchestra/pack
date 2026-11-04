package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/internal/storage"
	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/plan"
)

// pack run lists the actions of what is installed and runs one on its key, into the journal; a parameter is typed by
// the action's schema; restore, which the pack says is never run unasked, runs by the digest of its --dry-run.
func TestRun(t *testing.T) {
	d := releaseOf(t, generated(t), map[string]any{"observe": "true"})
	t.Cleanup(func() { runKey, runParams, runDry, runApprove = "", nil, false, "" })
	pack(t, d, "apply")

	if out := pack(t, d, "run"); !strings.Contains(out, "backup (postgres): Dumps the database") ||
		!strings.Contains(out, "--param to=… (string, required)") || !strings.Contains(out, "restore (postgres) [mutating, asks for approval]") {
		t.Fatalf("the list:\n%s", out)
	}
	out := pack(t, d, "run", "backup", "--param", "to=/backups/one")
	runParams = nil
	if !strings.Contains(out, "backup on postgres: done") || !strings.Contains(out, "tembo backup --to /backups/one --format custom") {
		t.Fatalf("backup:\n%s", out)
	}
	j := storage.FileJournal{Dir: dir.JournalPath(), Installation: must(dir.InstallationID())}
	runs := must(j.Runs())
	last := runs[len(runs)-1]
	if !strings.HasSuffix(last, "-backup") || !strings.Contains(fmtEntries(must(j.Read(last))), "action backup on postgres") {
		t.Errorf("the journal: %v", runs)
	}

	ctx := context.Background()
	inRelease(t, d)
	s := must(open(ctx, true))
	_, b, rel, err := s.Installed()
	if err != nil {
		t.Fatal(err)
	}
	a := must(session.FindAction(session.Actions(b, rel), "restore", ""))
	for _, bad := range [][]string{nil, {"from"}, {"to=/x"}, {"from=/x", "from2=/y"}} {
		if _, err := a.Params(bad); err == nil {
			t.Errorf("params %v: no refusal", bad)
		}
	}
	bk := must(session.FindAction(session.Actions(b, rel), "backup", ""))
	if _, err := bk.Params([]string{"to=/x", "format=zip"}); err == nil || !strings.Contains(err.Error(), "one of") {
		t.Errorf("a value not of the enum: %v", err)
	}
	p := must(a.Plan(b, rel, must(a.Params([]string{"from=/backups/one"}))))
	if err := approved(s, p, "", howRun(a, []string{"from=/backups/one"})); err == nil ||
		!strings.Contains(err.Error(), "pack run restore --param from=/backups/one --approve "+p.Digest) || p.Risks[0].Kind != plan.RiskAction {
		t.Errorf("restore under auto-safe: %v", err)
	}
	s.Close()

	runDry = true
	dry := pack(t, d, "run", "restore", "--param", "from=/backups/one", "--dry-run")
	runDry = false
	out = pack(t, d, "run", "restore", "--param", "from=/backups/one", "--approve", digestOf(t, dry))
	if !strings.Contains(out, "restore on postgres: done") {
		t.Errorf("restore --approve:\n%s", out)
	}
}

// pack status says what is installed and how its keys are; a key not ready is failed to pack next.
func TestStatus(t *testing.T) {
	d := releaseOf(t, generated(t), map[string]any{"observe": "true"})
	pack(t, d, "apply")
	if out := pack(t, d, "status"); !strings.Contains(out, "postgres: ready") || !strings.HasSuffix(out, "healthy\n") || strings.Contains(out, "not healthy") {
		t.Fatalf("status:\n%s", out)
	}
	if n := whatNext(context.Background()); n.State != api.StateApplied {
		t.Errorf("next, healthy: %+v", n)
	}
	write(t, d, "env.yaml", map[string]any{"target": "memory", "observe": "true", "unready": "postgres",
		"keys": map[string]any{"postgres": map[string]any{"resources": map[string]any{"requests": map[string]any{"memory": "2Gi"}}}}})
	if out := pack(t, d, "status"); !strings.Contains(out, "postgres: not ready") || !strings.Contains(out, "not healthy") {
		t.Errorf("status, postgres down:\n%s", out)
	}
	n := whatNext(context.Background())
	if n.State != api.StateFailed || n.Blocking[0].Code != api.CodeUnhealthy || !strings.Contains(n.Blocking[0].Message, "postgres: postgres is not ready: exited") ||
		n.Next[0] != "pack status" {
		t.Errorf("next, postgres down: %+v", n)
	}
}

func fmtEntries[E interface{ String() string }](es []E) string {
	var b strings.Builder
	for _, e := range es {
		b.WriteString(e.String() + "\n")
	}
	return b.String()
}
