package render_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"
)

// Refd ships a file of content/ byte for byte.
type Refd struct{}

func (*Refd) SetDefaults() {}
func (*Refd) Meta() sdk.Meta {
	return sdk.Meta{Name: "refd", Version: "1"} // no Dir: content/ is this file's own
}
func (*Refd) PackRoot() packschema.Root {
	return packschema.Root{Content: []packschema.File{{Path: "/etc/refd/dump.sql", Source: "dump.sql"}}}
}

func TestASourceFileOfARegistryPackIsADownload(t *testing.T) {
	dir := t.TempDir()
	if err := sdk.Generate(dir, &Refd{}); err != nil {
		t.Fatal(err)
	}
	ref := "oci://registry.example/packs/refd@sha256:" + strings.Repeat("0", 64)
	b, err := bundle.LoadRef(filepath.Join(dir, "refd"), ref)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := renderPack(b, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rel.Units) != 1 || len(rel.Units[0].Files) != 1 {
		t.Fatalf("units: %+v", rel.Units)
	}
	f := rel.Units[0].Files[0]
	if f.URL != ref+"/templates/dump.sql" {
		t.Errorf("url %q", f.URL)
	}
	if len(f.Content) == 0 { // the bytes stay: a target that fetches no files writes them as before
		t.Error("no content")
	}
}

func TestASourceFileOfADirectoryPackIsNoDownload(t *testing.T) {
	b := loadPack(t, &Refd{})
	rel, err := renderPack(b, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if f := rel.Units[0].Files[0]; len(f.URL) > 0 || len(f.Content) == 0 {
		t.Errorf("url %q, content %d", f.URL, len(f.Content))
	}
}
