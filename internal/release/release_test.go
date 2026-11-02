package release

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscovery(t *testing.T) {
	d := t.TempDir()
	write(t, d, "release.yaml", "pack: ../adh-show\nvalues: [extra.yaml]\n")
	write(t, d, "values.yaml", "a: 1\n")
	write(t, d, "kafka.values.yaml", "b: 2\n")
	write(t, d, "values/zk.yaml", "c: 3\n")
	write(t, d, "env.yaml", "target: docker\n")
	write(t, d, "env.prod.yaml", "target: k8s\n")

	w, err := Load(d, "")
	if err != nil {
		t.Fatal(err)
	}
	if w.Pack != "../adh-show" || filepath.Base(w.env) != "env.yaml" {
		t.Fatalf("%+v", w)
	}
	want := []string{"values.yaml", "kafka.values.yaml", "values", "extra.yaml"}
	if !slices.Equal(w.values, want) {
		t.Fatalf("values = %v, want %v", w.values, want)
	}
	files := w.ValuesFiles()
	if files[0] != filepath.Join(d, "values.yaml") {
		t.Fatalf("files = %v", files)
	}

	w, err = Load(d, "prod")
	if err != nil || filepath.Base(w.env) != "env.prod.yaml" {
		t.Fatalf("prod: %+v %v", w, err)
	}
	if _, err := Load(d, "nope"); err == nil {
		t.Fatal("an environment without a file must fail")
	}
}

func TestPackAndConfig(t *testing.T) {
	w := &Release{Pack: "p", env: "env.yaml"}
	if r, _ := w.PackRef(); r != "p" {
		t.Fatal("the release's pack")
	}
	if w.Config() != "env.yaml" {
		t.Fatal("the release's environment")
	}
	if _, err := (&Release{}).PackRef(); err == nil {
		t.Fatal("no pack: an error")
	}
}

func TestBadFile(t *testing.T) {
	d := t.TempDir()
	write(t, d, "release.yaml", "nmae: typo\n")
	if _, err := Load(d, ""); err == nil {
		t.Fatal("an unknown key in release.yaml must fail")
	}
}
