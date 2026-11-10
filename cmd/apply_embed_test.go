package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/horchestra/pack/pkg/saga"
)

func TestEmbeddedApply(t *testing.T) {
	dir := releaseOf(t, generated(t), nil)
	cache := t.TempDir()
	t.Setenv("PACK_CACHE", cache)
	heard := 0
	if err := Apply(context.Background(), ApplyOptions{
		Dir: dir, CacheDir: cache,
		Hear: func(_ saga.Entry) { heard++ },
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
}
