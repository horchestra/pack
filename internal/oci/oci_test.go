package oci_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/horchestra/pack/internal/oci"
	"github.com/horchestra/pack/internal/testpacks/regserver"
	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"
)

// Db is a pack with a file of content/; Shop holds one. Neither runs an image: the registry is theirs alone.
type Db struct {
	Port int    `json:"port,omitempty"`
	Conf string `json:"conf,omitempty" file:"/etc/db.conf" template:"db.ptmpl" parseAs:"text"`
}

func (*Db) SetDefaults() {}
func (*Db) Meta() sdk.Meta {
	return sdk.Meta{Name: "db", Version: "1.0.0", Artifact: "oci://packs/db", Dir: "testdata/db"}
}
func (*Db) PackRoot() packschema.Root { return packschema.Root{Ports: map[string]int{"sql": 5432}} }

type Shop struct {
	Title string `json:"title,omitempty"`
	DB    *Db    `json:"db,omitempty"`
}

func (*Shop) SetDefaults() {}
func (*Shop) Meta() sdk.Meta {
	return sdk.Meta{Name: "shop", Version: "2.0.0", Artifact: "oci://packs/shop", Dir: "testdata/shop"}
}
func (*Shop) PackRoot() packschema.Root { return packschema.Root{} }

func TestPull(t *testing.T) {
	_ = os.MkdirAll("testdata/db/content", 0o755)
	_ = os.WriteFile("testdata/db/content/db.ptmpl", []byte("port={{ .port }}\n"), 0o644)
	t.Cleanup(func() { _ = os.RemoveAll("testdata") })
	host := regserver.Serve(t, &Shop{})
	ctx := context.Background()
	o := oci.Options{PlainHTTP: true}
	ref := "oci://" + host + "/packs/shop:2.0.0"
	packs := filepath.Join(t.TempDir(), "packs")

	pin, err := oci.Pin(ctx, ref, o)
	if err != nil {
		t.Fatal(err)
	}
	p, err := oci.Pull(ctx, pin, packs, o)
	if err != nil {
		t.Fatal(err)
	}
	if p.Ref != pin || filepath.Base(p.Dir) != "shop" {
		t.Fatalf("pulled = %+v, pin = %s", p, pin)
	}
	// the pack and the pack it holds, side by side, each with its files
	for _, f := range []string{"shop/pack.yaml", "shop/schema.json", "db/pack.yaml", "db/schema.json", "db/templates/db.ptmpl"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(p.Dir), f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	// the bundle reads it as it reads the author's tree
	b, err := bundle.Load(p.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(b.Keys, []string{"db", "shop"}) || b.Version != "2.0.0" || b.Packs["db"].Version != "1.0.0" {
		t.Fatalf("keys %v, versions %s / %s", b.Keys, b.Version, b.Packs["db"].Version)
	}
	if string(b.Packs["db"].Templates["db.ptmpl"]) != "port={{ .port }}\n" {
		t.Fatalf("templates = %v", b.Packs["db"].Templates)
	}

	// by its digest it is content: pulled again, nothing is fetched (the server is gone)
	again, err := oci.Pull(ctx, pin, packs, oci.Options{PlainHTTP: true})
	if err != nil || again.Dir != p.Dir {
		t.Fatalf("again = %+v, %v", again, err)
	}
	if digestRef, err := oci.WithDigest(ref, p.Digest); err != nil || digestRef != pin {
		t.Fatalf("WithDigest = %s, %v", digestRef, err)
	}
}

func TestIsRef(t *testing.T) {
	for ref, want := range map[string]bool{
		"oci://reg.example.com/packs/shop:1": true, "oci://localhost:5000/shop:1": true,
		"./shop": false, "/abs/shop": false, "oci://./layout:shop:1": false, "shop": false,
	} {
		if got := oci.IsRef(ref); got != want {
			t.Errorf("IsRef(%s) = %v", ref, got)
		}
	}
}
