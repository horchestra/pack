package remote

import (
	"testing"

	"github.com/horchestra/pack/pkg/render"
)

// A target that fetches files gets no bytes of them over the wire; every other file keeps its content.
func TestStripFetched(t *testing.T) {
	u := &render.Unit{Files: []render.File{
		{Path: "/a", URL: "oci://registry.example/packs/p@sha256:d/templates/a", Content: []byte("a")},
		{Path: "/b", Content: []byte("b")},
		{Path: "/c", URL: "http://upstream/c"}, // the author's own URL never had content
		{Path: "/e", URL: "file:///tmp/pack/templates/e", Content: []byte("e")},
	}, Packages: map[string]render.Packages{
		"debian": {Files: []render.File{{Path: "/d", URL: "oci://packs/p:1/templates/d", Content: []byte("d")}}},
	}}
	out := stripFetched(u)
	if len(out.Files[0].Content) > 0 {
		t.Error("a: content went with the URL")
	}
	if string(out.Files[1].Content) != "b" {
		t.Error("b: content stripped without a URL")
	}
	if got := out.Packages["debian"].Files[0].Content; len(got) > 0 {
		t.Error("d: content went with the URL")
	}
	if len(out.Files[3].Content) > 0 {
		t.Error("e: content went with the file:// URL")
	}
	// the unit itself is untouched
	if len(u.Files[0].Content) == 0 || len(u.Packages["debian"].Files[0].Content) == 0 {
		t.Error("the unit was mutated")
	}
	if stripFetched(nil) != nil {
		t.Error("nil")
	}
}
