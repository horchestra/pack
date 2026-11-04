package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/horchestra/pack/internal/diagnose"
	"github.com/horchestra/pack/internal/storage"
	"github.com/horchestra/pack/pkg/saga"
)

// fail makes the record of d say its last saga failed at key with err, the journal holding lines of it.
func failSaga(t *testing.T, d, key, err string, lines ...string) {
	t.Helper()
	inRelease(t, d)
	st := dir.FileState()
	rec, e := st.Load()
	if e != nil || rec == nil {
		t.Fatal(rec, e)
	}
	rec.State = saga.Failed
	rec.Saga = &saga.Progress{Installation: rec.Installation, ID: "s1", Error: key + ": " + err}
	if e := st.Save(rec); e != nil {
		t.Fatal(e)
	}
	j := storage.FileJournal{Dir: dir.JournalPath(), Installation: rec.ID}
	for _, l := range lines {
		_ = j.Append(saga.Entry{Run: "s1", Kind: saga.KindLog, Node: key, Message: l})
	}
	_ = j.Append(saga.Entry{Run: "s1", Kind: saga.KindFail, Node: key, Error: err})
}

func TestDiagnose(t *testing.T) {
	logs := filepath.Join(t.TempDir(), "logs.json")
	if err := os.WriteFile(logs, []byte(`{"postgres": {"postgres-0": ["LOG:  starting PostgreSQL", "FATAL:  could not create listen socket"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d := releaseOf(t, generated(t), map[string]any{"logs": logs})
	pack(t, d, "apply")
	failSaga(t, d, "postgres", "apply postgres: exited", "starting", "could not bind 0.0.0.0:5433: address already in use")
	t.Cleanup(func() { diagnoseApply, jsonOut = "", false })
	out := pack(t, d, "diagnose", "--json")
	jsonOut = false
	var c diagnose.Case
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatalf("diagnose --json: %v\n%s", err, out)
	}
	if c.Key != "postgres" || c.Diagnosis != "port-taken" || len(c.Remedies) != 2 || c.Remedies[0].Patch != "/postgres/pg_port" ||
		c.Remedies[0].Value != float64(5434) || c.Remedies[1].Command != "pack discover" ||
		!strings.Contains(strings.Join(c.Evidence, "\n"), "postgres-0: FATAL:  could not create listen socket") {
		t.Fatalf("the case: %+v", c)
	}
	inRelease(t, d)
	if n := whatNext(context.Background()); n.State != StateFailed || n.Next[1] != "pack diagnose --apply r1" || !strings.Contains(n.Blocking[0].Message, "port-taken") {
		t.Errorf("next of a failed saga: %+v", n)
	}
	pack(t, d, "diagnose", "--apply", "r1")
	text, _ := os.ReadFile(filepath.Join(d, "values.yaml"))
	if !strings.Contains(string(text), "# source: diagnose — postgres cannot listen") || !strings.Contains(string(text), "pg_port: 5434") {
		t.Errorf("the way out taken:\n%s", text)
	}
}

func TestDiagnoseKinds(t *testing.T) {
	for err, want := range map[string]string{
		"Error response from daemon: pull access denied for x": diagnose.ImagePull,
		"container killed: OOMKilled":                          diagnose.OOM,
		"write /data: no space left on device":                 diagnose.Disk,
		"did not become ready within 5m":                       diagnose.Timeout,
		"open /etc/x: permission denied":                       diagnose.Permission,
		"listen tcp :80: bind: address already in use":         diagnose.PortConflict,
		"something nobody has seen":                            diagnose.Unknown,
	} {
		c := diagnose.Make(diagnose.Input{Key: "k", Error: err})
		if c.Class != want {
			t.Errorf("%q: %s, want %s", err, c.Class, want)
		}
		if want == diagnose.Unknown && len(c.Remedies) > 0 {
			t.Errorf("an unknown case offers a way out: %+v", c.Remedies)
		}
	}
	long := make([]string, 100)
	for i := range long {
		long[i] = strings.Repeat("x", 500)
	}
	c := diagnose.Make(diagnose.Input{Error: "e", Lines: long})
	if !c.Truncated || len(c.Evidence) != 40 || len(c.Evidence[0]) > 310 {
		t.Errorf("evidence bounded: %d lines, %v", len(c.Evidence), c.Truncated)
	}
}

func TestEvidenceCutsAtACharacter(t *testing.T) {
	c := diagnose.Make(diagnose.Input{Error: "e", Lines: []string{strings.Repeat("ж", 400)}})
	if l := c.Evidence[0]; !utf8.ValidString(l) || !strings.HasSuffix(l, "…") {
		t.Errorf("a line cut inside a character: %q", l[len(l)-8:])
	}
}
