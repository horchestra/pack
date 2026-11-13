package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openbao "github.com/horchestra/pack/examples/openbao/go"
	"github.com/horchestra/pack/pkg/saga"
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

// TestEmbeddedApplyPackInMemory is the apply of a pack the process holds as Go: no pack directory is generated,
// no release.yaml names it, no values file is read — the pack and the values are the caller's own.
func TestEmbeddedApplyPackInMemory(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "env.yaml", map[string]any{"target": "memory"})
	cache := t.TempDir()
	if err := Apply(context.Background(), ApplyOptions{
		Dir: dir, CacheDir: cache,
		Pack:   &openbao.OpenBao{},
		Values: map[string]any{"openbao": map[string]any{"api_addr": "https://bao:8200"}},
	}); err != nil {
		t.Fatal(err)
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
