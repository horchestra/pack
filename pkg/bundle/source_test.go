package bundle

import (
	"path/filepath"
	"testing"
)

func TestDirOfADotAndOfATrailingSlash(t *testing.T) {
	d := t.TempDir()
	t.Chdir(d)
	for _, in := range []string{".", d + "/"} {
		got := Dir(in).(dirSource).dir
		if !filepath.IsAbs(got) || got != filepath.Clean(got) {
			t.Fatalf("Dir(%q) = %q: must be absolute and clean, so the siblings are found beside it", in, got)
		}
	}
}
