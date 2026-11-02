package sdk_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/horchestra/pack/internal/testpacks/openbao"
)

// goldenDir holds the artifacts of the test packs as this SDK writes them. The SDKs of the other languages
// (python/, java/) generate the same two packs and are compared with these files, so the three agree on the format.
const goldenDir = "testdata/golden"

// UPDATE_GOLDEN=1 go test ./sdk -run Golden rewrites the files after a deliberate change of the format.
func TestGoldenArtifacts(t *testing.T) {
	got := generate(t, &openbao.OpenBao{})
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.RemoveAll(goldenDir); err != nil {
			t.Fatal(err)
		}
		for _, rel := range tree(t, got) {
			dst := filepath.Join(goldenDir, rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dst, read(t, got, rel), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	want, have := tree(t, goldenDir), tree(t, got)
	if !slices.Equal(want, have) {
		t.Fatalf("files:\n golden %v\n got    %v\n(UPDATE_GOLDEN=1 rewrites the golden files)", want, have)
	}
	for _, rel := range want {
		if string(read(t, goldenDir, rel)) != string(read(t, got, rel)) {
			t.Errorf("%s differs from %s (UPDATE_GOLDEN=1 rewrites it)", rel, filepath.Join(goldenDir, rel))
		}
	}
}
