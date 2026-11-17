package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openbao "github.com/horchestra/pack/examples/openbao/go"
	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"
)

func TestEmbeddedApply(t *testing.T) {
	dir := releaseOf(t, generated(t), nil)
	cache := t.TempDir()
	t.Setenv("PACK_CACHE", cache)
	t.Setenv("PACK_EMBED_TEST", "before")
	heard := 0
	if err := Apply(context.Background(), ApplyOptions{
		Dir: dir, CacheDir: cache, Refresh: true,
		Env: map[string]string{"PACK_EMBED_TEST": "during"},
		Hear: func(_ saga.Entry) {
			heard++
			if got := os.Getenv("PACK_EMBED_TEST"); got != "during" {
				t.Errorf("PACK_EMBED_TEST during apply = %q", got)
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	if heard == 0 {
		t.Fatal("the embedded apply did not report saga entries")
	}
	if _, err := os.Stat(filepath.Join(dir, ".pack", "state.json")); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("PACK_CACHE"); got != cache {
		t.Fatalf("PACK_CACHE = %q, want %q", got, cache)
	}
	if got := os.Getenv("PACK_EMBED_TEST"); got != "before" {
		t.Fatalf("PACK_EMBED_TEST = %q, want %q", got, "before")
	}
}

type embeddedVersionedPack struct {
	version string
	dir     string
}

func (*embeddedVersionedPack) SetDefaults() {}
func (p *embeddedVersionedPack) Meta() sdk.Meta {
	return sdk.Meta{Name: "versioned", Version: p.version, Dir: p.dir}
}
func (*embeddedVersionedPack) PackRoot() packschema.Root { return packschema.Root{} }

func TestEmbeddedApplyPackUpgradeUsesRecordedVersion(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "env.yaml", map[string]any{"target": "memory"})
	cache := t.TempDir()
	pack := &embeddedVersionedPack{version: "1.0.0", dir: t.TempDir()}
	if err := Apply(context.Background(), ApplyOptions{Dir: dir, CacheDir: cache, Pack: pack}); err != nil {
		t.Fatal(err)
	}

	pack.version = "2.0.0"
	var planned *plan.Plan
	if err := Apply(context.Background(), ApplyOptions{
		Dir: dir, CacheDir: cache, Pack: pack,
		Planned: func(p *plan.Plan) { planned = p },
	}); err != nil {
		t.Fatal(err)
	}
	if planned == nil || len(planned.Steps) != 1 || planned.Steps[0].Kind != plan.Apply {
		t.Fatalf("the upgrade must apply the new version: %+v", planned)
	}
	if len(planned.Changes) != 1 || planned.Changes[0].From != "1.0.0" || planned.Changes[0].To != "2.0.0" {
		t.Fatalf("the upgrade is not from the recorded version: %+v", planned.Changes)
	}
}

func TestEmbeddedApplyRefusesARecordWithoutSnapshot(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "env.yaml", map[string]any{"target": "memory"})
	if err := os.MkdirAll(filepath.Join(dir, ".pack"), 0o700); err != nil {
		t.Fatal(err)
	}
	record := saga.Record{
		ID:           "5d50a133-42e2-4b7f-93c2-f0c0a6c48d17",
		Installation: saga.Installation{Pack: "sdk://versioned", Version: "1.0.0"},
		State:        saga.Installed,
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".pack", "state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	pack := &embeddedVersionedPack{version: "2.0.0", dir: t.TempDir()}
	err = Apply(context.Background(), ApplyOptions{Dir: dir, CacheDir: t.TempDir(), Pack: pack})
	if err == nil || !strings.Contains(err.Error(), "without a cache snapshot") {
		t.Fatalf("an old sdk:// record must not be read as the current version: %v", err)
	}
}

// TestEmbeddedApplyPackInMemory is the apply of a pack the process holds as Go: no pack directory is generated in
// the release, no release.yaml names it, no values file is read — the pack and the values are the caller's own.
func TestEmbeddedApplyPackInMemory(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "env.yaml", map[string]any{"target": "memory"})
	cache := t.TempDir()
	pack := &openbao.OpenBao{}
	values := map[string]any{"openbao": map[string]any{"api_addr": "https://bao:8200"}}
	if err := Apply(context.Background(), ApplyOptions{
		Dir: dir, CacheDir: cache,
		Pack: pack, Values: values,
	}); err != nil {
		t.Fatal(err)
	}
	var repeated *plan.Plan
	if err := Apply(context.Background(), ApplyOptions{
		Dir: dir, CacheDir: cache, Pack: pack, Values: values,
		Planned: func(p *plan.Plan) { repeated = p },
	}); err != nil {
		t.Fatal(err)
	}
	if repeated == nil || len(repeated.Steps) != 0 {
		t.Fatalf("the same embedded pack must not be applied again: %+v", repeated)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "packs" || strings.HasPrefix(e.Name(), "values.") || strings.HasPrefix(e.Name(), "release.") {
			t.Fatalf("the apply wrote what it was given as Go: %s", e.Name())
		}
	}
}
