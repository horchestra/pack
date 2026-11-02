package openbao

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/horchestra/pack/sdk"
)

// golden is what all three SDKs generate for this pack: this test writes it (UPDATE_GOLDEN=1), the Python and the
// Java examples are compared with it.
const golden = "../golden"

func tree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	slices.Sort(out)
	return out
}

func TestGolden(t *testing.T) {
	got := t.TempDir()
	if err := sdk.Generate(got, &OpenBao{}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.RemoveAll(golden); err != nil {
			t.Fatal(err)
		}
		for _, rel := range tree(t, got) {
			b, _ := os.ReadFile(filepath.Join(got, rel))
			dst := filepath.Join(golden, rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dst, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	want, have := tree(t, golden), tree(t, got)
	if !slices.Equal(want, have) {
		t.Fatalf("files:\n golden %v\n got    %v", want, have)
	}
	for _, rel := range want {
		a, _ := os.ReadFile(filepath.Join(golden, rel))
		b, _ := os.ReadFile(filepath.Join(got, rel))
		if string(a) != string(b) {
			t.Errorf("%s differs from the golden file (UPDATE_GOLDEN=1 rewrites it)", rel)
		}
	}
}
