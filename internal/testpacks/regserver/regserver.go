// Package regserver serves the packs an author wrote — what sdk.Generate makes of a root pack — from a registry
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

	"github.com/horchestra/pack/sdk"
	"github.com/opencontainers/go-digest"
	ocispecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"sigs.k8s.io/yaml"

	packer "github.com/arenadata/oci-packer"
	"github.com/arenadata/oci-packer/pkg/registry/oci-layout"
	"github.com/arenadata/oci-packer/pkg/registry/regtest"
	"github.com/horchestra/pack/pkg/format"
)

// Serve makes a registry that holds the pack the sdk generated, built by oci-packer. The dependency is built first;
// the mount of it in the pack that holds it is what a registry does with an oci:// item (the member lands in the
// index, manifest and blobs), done here by hand, since a layout resolves no repository by name.
func Serve(t testing.TB, root sdk.Pack) (host string) {
	t.Helper()
	ctx := context.Background()
	gen := t.TempDir()
	if err := sdk.Generate(gen, root); err != nil {
		t.Fatal(err)
	}
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
	all, err := sdk.Packs(root)
	if err != nil {
		t.Fatal(err)
	}
	var held []ocispecv1.Descriptor
	for _, p := range all[1:] {
		_, d := build(p.Meta().Name, p.Meta().Version)
		held = append(held, d)
	}
	res, _ := build(all[0].Meta().Name, all[0].Meta().Version, held...)
	return regtest.Host(regtest.Serve(t, res, "packs/"+all[0].Meta().Name))
}
