package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/saga"
)

// pack next follows a release from nothing to installed, one state after another.
func TestNext(t *testing.T) {
	ctx := context.Background()
	empty := t.TempDir()
	inRelease(t, empty)
	if n := whatNext(ctx); n.State != StateNew || !strings.HasPrefix(n.Next[0], "pack init") {
		t.Errorf("no release: %+v", n)
	}

	d := releaseOf(t, generated(t), nil)
	write(t, d, "values.yaml", map[string]any{"postgres": map[string]any{"id": "n1"}}) // openbao.api_addr is required
	inRelease(t, d)
	n := whatNext(ctx)
	if n.State != StateUnfilled || len(n.Blocking) != 1 || n.Blocking[0].Code != api.CodeValuesMissing || !strings.Contains(n.Next[1], "/openbao/api_addr") {
		t.Fatalf("a required setting not given: %+v", n)
	}

	write(t, d, "values.yaml", map[string]any{"openbao": map[string]any{"api_addr": "https://bao:8200", "log_level": 7}})
	if n := whatNext(ctx); n.State != StateInvalid || n.Blocking[0].Code != api.CodeValuesInvalid {
		t.Errorf("a value the schema refuses: %+v", n)
	}

	write(t, d, "values.yaml", map[string]any{"openbao": map[string]any{"api_addr": "https://bao:8200"}})
	if n := whatNext(ctx); n.State != StatePlanned || n.Next[len(n.Next)-1] != "pack apply" {
		t.Errorf("valid, not installed: %+v", n)
	}
	pack(t, d, "apply")
	inRelease(t, d)
	if n := whatNext(ctx); n.State != StateApplied || len(n.Next) != 0 {
		t.Errorf("installed: %+v", n)
	}

	jsonOut = false
	t.Cleanup(func() { jsonOut = false })
	out := pack(t, d, "next", "--json")
	jsonOut = false
	var got nextState
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.State != StateApplied {
		t.Errorf("--json: %v\n%s", err, out)
	}
}

func TestNextUnrefreshed(t *testing.T) {
	d := releaseOf(t, "oci://registry.invalid/packs/openbao:1", map[string]any{"target": "fake"})
	t.Setenv("PACK_TARGET_PATH", t.TempDir())
	inRelease(t, d)
	n := whatNext(context.Background())
	if n.State != StateUnrefreshed || len(n.Blocking) != 2 || n.Next[0] != "pack refresh" {
		t.Errorf("not locked, no program: %+v", n)
	}
	_ = os.Remove(filepath.Join(d, "release.lock.json"))
}

func TestNextFailed(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	pack(t, d, "apply")
	inRelease(t, d)
	st := dir.FileState()
	rec, err := st.Load()
	if err != nil || rec == nil {
		t.Fatal(rec, err)
	}
	rec.State = saga.Failed
	rec.Saga = &saga.Progress{Installation: rec.Installation, Error: "openbao: boom"}
	if err := st.Save(rec); err != nil {
		t.Fatal(err)
	}
	n := whatNext(context.Background())
	if n.State != StateFailed || n.Blocking[0].Code != api.CodeSagaUnfinished || !strings.Contains(n.Blocking[0].Message, "boom") || n.Next[0] != "pack diagnose" {
		t.Errorf("a failed saga: %+v", n)
	}
}

// An environment that does not read is no matter of refresh: next says error, with what to mend.
func TestNextBrokenEnvironment(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	if err := os.WriteFile(filepath.Join(d, "env.yaml"), []byte("target: [not, a, name\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inRelease(t, d)
	if n := whatNext(context.Background()); n.State != StateError || len(n.Blocking) != 1 || slices.Contains(n.Next, "pack refresh") {
		t.Errorf("a broken env.yaml: %+v", n)
	}
}
