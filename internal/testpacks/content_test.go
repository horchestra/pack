package testpacks_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target/memory"
	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"
)

// Inner is a pack another holds; Outer holds it. Each has one template that says everything it sees.
type Inner struct {
	Name     string     `json:"name,omitempty"`
	Password sdk.Secret `json:"password" env:"INNER_PASSWORD"`
}

func (*Inner) SetDefaults() {}
func (i *Inner) Meta() sdk.Meta {
	return sdk.Meta{Name: "inner", Version: "1", Image: "images/inner:1", Dir: innerDir}
}
func (*Inner) PackRoot() packschema.Root {
	return packschema.Root{Content: []packschema.File{{Path: "/inner", Template: "seen"}}}
}

type Outer struct {
	Title string `json:"title,omitempty"`
	Fixed string `json:"fixed,omitempty" jsonschema:"readOnly=true"`
	Inner *Inner `json:"held,omitempty"`
}

func (*Outer) SetDefaults() {}
func (*Outer) Meta() sdk.Meta {
	return sdk.Meta{Name: "outer", Version: "1", Image: "images/outer:1", Dir: outerDir}
}
func (*Outer) PackRoot() packschema.Root {
	return packschema.Root{Content: []packschema.File{{Path: "/outer", Template: "seen"}}}
}

var innerDir, outerDir string

func load(t *testing.T) *bundle.Bundle {
	t.Helper()
	root := t.TempDir()
	innerDir, outerDir = filepath.Join(root, "i"), filepath.Join(root, "o")
	for _, d := range []string{innerDir, outerDir} {
		if err := os.MkdirAll(filepath.Join(d, "content"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	seen := "{{ . | toJson }}\n"
	for _, d := range []string{innerDir, outerDir} {
		if err := os.WriteFile(filepath.Join(d, "content", "seen"), []byte(seen), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := t.TempDir()
	if err := sdk.Generate(out, &Outer{}); err != nil {
		t.Fatal(err)
	}
	b, err := bundle.Load(filepath.Join(out, "outer"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func renderOf(b *bundle.Bundle, vals map[string]any) (*render.Release, error) {
	return render.Render(b, render.Values{Values: vals}, render.Context{Installation: "t", Target: memory.New()})
}

// A template sees the values of the packs its pack holds, under the fields of the release's contract, and none of
// their passwords; the pack held sees nothing of the pack that holds it.
func TestATemplateSeesDownOnly(t *testing.T) {
	rel, err := renderOf(load(t), map[string]any{
		"title": "up here",
		"held":  map[string]any{"name": "down there", "password": map[string]any{"secretRef": map[string]any{"name": "s", "key": "k"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, u := range rel.Units {
		for _, f := range u.Files {
			files[f.Path] = string(f.Content)
		}
	}
	outer, inner := files["/outer"], files["/inner"]
	if !strings.Contains(outer, `"title":"up here"`) || !strings.Contains(outer, `"held":{"name":"down there"}`) {
		t.Errorf("the pack that holds sees its own and what it holds, no password: %s", outer)
	}
	if !strings.Contains(inner, `"name":"down there"`) || strings.Contains(inner, "up here") || strings.Contains(inner, "title") {
		t.Errorf("the pack held sees only its own: %s", inner)
	}
}

// readOnly is the tag's, and a value the operator writes for it is refused.
func TestAReadOnlyFieldIsRefused(t *testing.T) {
	_, err := renderOf(load(t), map[string]any{"fixed": "mine"})
	if err == nil || !strings.Contains(err.Error(), "/outer/fixed is read-only") {
		t.Errorf("a read-only field the operator wrote: %v", err)
	}
}
