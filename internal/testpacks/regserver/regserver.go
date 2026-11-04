// Package regserver serves the packs an author wrote — what sdk.Generate made of a root pack, a directory — from a registry
// built for the test: oci-packer packs each of them into a layout, the pack that holds others gets them as members
// of its index (what a registry does with an oci:// item; a layout resolves no repository by name, so it is done
// here), and regtest serves the layout over the distribution API. One level of held packs.
package regserver

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"sigs.k8s.io/yaml"

	packer "github.com/arenadata/oci-packer"
	layout "github.com/arenadata/oci-packer/pkg/registry/oci-layout"
	"github.com/arenadata/oci-packer/pkg/registry/regtest"

	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/format"
)

// Serve makes a registry that holds the pack the sdk generated, built by oci-packer. The dependency is built first;
// the mount of it in the pack that holds it is what a registry does with an oci:// item (the member lands in the
// index, manifest and blobs), done here by hand, since a layout resolves no repository by name.
func Serve(t testing.TB, pack string) (host string) {
	t.Helper()
	ctx := context.Background()
	gen := filepath.Dir(pack) // the generated packs: the pack and those it holds, side by side
	lay := filepath.Join(t.TempDir(), "layout")
	build := func(name, version string, mounted ...ocispecv1.Descriptor) (*layout.Layout, ocispecv1.Descriptor) {
		r, err := layout.New("oci://" + lay + ":packs/" + name + ":" + version)
		if err != nil {
			t.Fatal(err)
		}
		res := r.(*layout.Layout)
		raw, err := os.ReadFile(filepath.Join(gen, name, "pack.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var p packer.Pack
		if err := yaml.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		p.Items = slices.DeleteFunc(p.Items, func(it packer.Descriptor) bool { return strings.HasPrefix(it.From, "oci://") })
		t.Chdir(filepath.Join(gen, name)) // the items are relative to the pack's directory
		desc, err := packer.Build(ctx, p, res)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(mounted) > 0 {
			// the pack's own files are one member of the index, the packs it holds the others
			own := desc
			own.ArtifactType = format.ArtifactSchema
			idx := ocispecv1.Index{
				SchemaVersion: 2, MediaType: ocispecv1.MediaTypeImageIndex, ArtifactType: p.Type,
				Manifests: append([]ocispecv1.Descriptor{own}, mounted...), Annotations: p.Annotations,
			}
			body, _ := json.Marshal(idx)
			desc = ocispecv1.Descriptor{MediaType: ocispecv1.MediaTypeImageIndex, ArtifactType: p.Type,
				Digest: digest.FromBytes(body), Size: int64(len(body)), Annotations: p.Annotations}
			if err := res.Push(ctx, desc, bytes.NewReader(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := res.SetTag(ctx, desc); err != nil {
			t.Fatal(err)
		}
		return res, desc
	}
	// the pack and the packs it holds, as the generator wrote them side by side: the held ones built first
	meta := func(dir string) (name, version string) {
		raw, err := os.ReadFile(filepath.Join(gen, dir, "pack.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var p packer.Pack
		if err := yaml.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		return p.Annotations[format.AnnotationName], p.Annotations[format.AnnotationVersion]
	}
	entries, err := os.ReadDir(gen)
	if err != nil {
		t.Fatal(err)
	}
	var held []ocispecv1.Descriptor
	for _, e := range entries {
		if e.IsDir() && e.Name() != filepath.Base(pack) {
			_, d := build(meta(e.Name()))
			held = append(held, d)
		}
	}
	name, version := meta(filepath.Base(pack))
	res, _ := build(name, version, held...)
	return regtest.Host(regtest.Serve(t, res, "packs/"+name))
}

// ServePack is Serve of a pack a test writes in Go: generated first.
func ServePack(t testing.TB, root sdk.Pack) (host string) {
	t.Helper()
	gen := t.TempDir()
	if err := sdk.Generate(gen, root); err != nil {
		t.Fatal(err)
	}
	return Serve(t, filepath.Join(gen, root.Meta().Name))
}
