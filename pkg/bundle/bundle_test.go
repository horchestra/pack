package bundle_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/horchestra/pack/internal/testpacks"
	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"
)

func load(t *testing.T, root sdk.Pack) *bundle.Bundle {
	t.Helper()
	dir := t.TempDir()
	if err := sdk.Generate(dir, root); err != nil {
		t.Fatal(err)
	}
	return loadDir(t, filepath.Join(dir, root.Meta().Name))
}

// openbaoBundle is the test pack that holds another, as the sdk generated it.
func openbaoBundle(t *testing.T) *bundle.Bundle {
	t.Helper()
	return loadDir(t, testpacks.Generated(t, t.TempDir()))
}

func loadDir(t *testing.T, dir string) *bundle.Bundle {
	t.Helper()
	b, err := bundle.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Inner is held by Mid, Mid by Outer: three levels.
type Inner struct {
	ID string `json:"id,omitempty"`
}

func (*Inner) SetDefaults() {}
func (*Inner) Meta() sdk.Meta {
	return sdk.Meta{Name: "inner", Version: "1", Dir: "/nonexistent"}
}
func (*Inner) PackRoot() packschema.Root { return packschema.Root{} }

type Mid struct {
	Name  string `json:"name,omitempty"`
	Inner *Inner `json:"inner,omitempty"`
}

func (*Mid) SetDefaults() {}
func (*Mid) Meta() sdk.Meta {
	return sdk.Meta{Name: "mid", Version: "1", Dir: "/nonexistent"}
}
func (*Mid) PackRoot() packschema.Root { return packschema.Root{} }

type Outer struct {
	Title string `json:"title"`
	Mid   *Mid   `json:"mid,omitempty"`
}

func (*Outer) SetDefaults() {}
func (*Outer) Meta() sdk.Meta {
	return sdk.Meta{Name: "outer", Version: "1", Dir: "/nonexistent"}
}
func (*Outer) PackRoot() packschema.Root { return packschema.Root{} }

// The instances: the pack itself, and each pack held in a field — held ones before the one that holds them.
func TestInstances(t *testing.T) {
	b := openbaoBundle(t)
	if !slices.Equal(b.Keys, []string{"postgres", "openbao"}) {
		t.Fatalf("keys = %v", b.Keys)
	}
	root, held := b.Instances["openbao"], b.Instances["postgres"]
	if len(root.Path) > 0 || len(root.Parent) > 0 || root.Pack != "openbao" {
		t.Fatalf("root = %+v", root)
	}
	if held.Path != "/postgres" || held.Parent != "openbao" || held.Pack != "tembo" || held.Hold.Required {
		t.Fatalf("held = %+v", held)
	}

	d := load(t, &Outer{})
	if !slices.Equal(d.Keys, []string{"mid.inner", "mid", "outer"}) {
		t.Fatalf("keys = %v: below the first level a key is parent.field", d.Keys)
	}
	if in := d.Instances["mid.inner"]; in.Path != "/mid/inner" || in.Parent != "mid" {
		t.Fatalf("inner = %+v", in)
	}
}

// The pack's own schema has its settings and not those of the pack it holds; its order is the author's.
func TestOwnSchemaCutsOutWhatIsHeld(t *testing.T) {
	b := openbaoBundle(t)
	root := b.Packs["openbao"].Schema.Root
	if _, held := root.Properties["postgres"]; held {
		t.Fatal("the held pack is not a property of the pack's own schema")
	}
	if !slices.Equal(root.Order, []string{"openbao"}) {
		t.Fatalf("order = %v", root.Order)
	}
	if _, ok := b.Composition.Root.Properties["postgres"]; !ok {
		t.Fatal("the flat view has it as a key of its own")
	}
}

// The operator's document, taken apart by instance.
func TestSplit(t *testing.T) {
	b := load(t, &Outer{})
	op := map[string]any{
		"title": "t",
		"mid":   map[string]any{"name": "m", "inner": map[string]any{"id": "n1"}},
	}
	got := b.Split(op)
	if got["outer"]["title"] != "t" || got["outer"]["mid"] != nil {
		t.Fatalf("outer = %v: its own, without the pack it holds", got["outer"])
	}
	if got["mid"]["name"] != "m" || got["mid"]["inner"] != nil {
		t.Fatalf("mid = %v", got["mid"])
	}
	if got["mid.inner"]["id"] != "n1" {
		t.Fatalf("inner = %v", got["mid.inner"])
	}
	// a pack nobody named is not there
	if _, ok := b.Split(map[string]any{"title": "t"})["mid"]; ok {
		t.Fatal("mid was not named")
	}
	// ... even as {}, naming is the ask
	if _, ok := b.Split(map[string]any{"title": "t", "mid": map[string]any{}})["mid"]; !ok {
		t.Fatal("mid named as {} is there")
	}
}

// The operator's pointers and the flat view's, both ways.
func TestInternalAndExternal(t *testing.T) {
	b := load(t, &Outer{})
	for op, flat := range map[string]string{
		"/title":          "/outer/title",
		"/mid/name":       "/mid/name",
		"/mid/inner/id":   "/mid.inner/id",
		"/mid/inner":      "/mid.inner",
		"/midlife/crisis": "/outer/midlife/crisis", // a prefix of a name is not the pack
	} {
		if got := b.Internal(op); got != flat {
			t.Errorf("Internal(%s) = %s, want %s", op, got, flat)
		}
	}
	for flat, op := range map[string]string{
		"/outer/title":  "/title",
		"/mid/name":     "/mid/name",
		"/mid.inner/id": "/mid/inner/id",
		"/outer":        "",
		"/unknown/x":    "/unknown/x",
	} {
		if got := b.External(flat); got != op {
			t.Errorf("External(%s) = %s, want %s", flat, got, op)
		}
	}
}

// A pack pulled from a registry is named by its reference, and the packs it holds by the items of its pack file.
func TestLoadRef(t *testing.T) {
	dir := testpacks.Generated(t, t.TempDir())
	ref := "oci://registry.example/packs/openbao@sha256:" + strings.Repeat("0", 64)
	b, err := bundle.LoadRef(dir, ref)
	if err != nil {
		t.Fatal(err)
	}
	if b.Root.Ref != ref {
		t.Errorf("root ref %q", b.Root.Ref)
	}
	if got := b.Packs["tembo"].Ref; got != "oci://packs/tembo:0.2.0" {
		t.Errorf("tembo ref %q", got)
	}
	// a directory names nothing
	if d := loadDir(t, dir); len(d.Root.Ref) > 0 || len(d.Packs["tembo"].Ref) > 0 {
		t.Errorf("refs %q, %q", d.Root.Ref, d.Packs["tembo"].Ref)
	}
}
