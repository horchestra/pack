package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func initFlags(t *testing.T, target string, force bool) {
	t.Helper()
	initTarget, initSource, initImage, initRuntime, initLang, initForce = target, "", "", "", "", force
	t.Cleanup(func() {
		initTarget, initSource, initImage, initRuntime, initLang, initForce = "", "", "", "", "", false
	})
}

func TestInit(t *testing.T) {
	ctx := context.Background()
	pk := generated(t)
	out := filepath.Join(t.TempDir(), "rel")
	t.Chdir(t.TempDir()) // initRelease goes into the release's directory; the test's is given back after
	initFlags(t, "memory", false)
	said, err := initRelease(ctx, pk, out, strings.NewReader(""), false)
	if err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(said, "\n"))
	}
	for _, f := range []string{"release.yaml", "env.yaml", "values.yaml", "AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	var env map[string]any
	raw, _ := os.ReadFile(filepath.Join(out, "env.yaml"))
	if err := yaml.Unmarshal(raw, &env); err != nil || env["target"] != "memory" {
		t.Errorf("env.yaml: %v %v", env, err)
	}
	values, _ := os.ReadFile(filepath.Join(out, "values.yaml"))
	var v map[string]any
	if err := yaml.Unmarshal(values, &v); err != nil || len(v) > 0 {
		t.Errorf("values.yaml has values of its own, or does not read: %v %v", v, err)
	}
	if !strings.Contains(string(values), "#log_level: info") {
		t.Errorf("values.yaml:\n%s", values)
	}
	// written by values write: the same file
	inRelease(t, out)
	valuesFlags(t, false, true)
	if _, err := writeValues(ctx); err != nil {
		t.Errorf("values write --check after init: %v", err)
	}

	if _, err := initRelease(ctx, pk, out, strings.NewReader(""), false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("over a release: %v", err)
	}
	initFlags(t, "memory", true)
	if _, err := initRelease(ctx, pk, out, strings.NewReader(""), false); err != nil {
		t.Errorf("--force: %v", err)
	}
}

func TestInitAsksTheTarget(t *testing.T) {
	pk := generated(t)
	t.Chdir(t.TempDir())
	initFlags(t, "", false)
	if _, err := initRelease(context.Background(), pk, filepath.Join(t.TempDir(), "a"), strings.NewReader(""), false); err == nil ||
		!strings.Contains(err.Error(), "--target") {
		t.Errorf("no terminal, no --target: %v", err)
	}
	if got, err := askTarget(strings.NewReader("\n")); err != nil || got != "docker" {
		t.Errorf("an empty answer: %q %v", got, err)
	}
	if got, err := askTarget(strings.NewReader("k8s\n")); err != nil || got != "k8s" {
		t.Errorf("k8s: %q %v", got, err)
	}
	if _, err := initRelease(context.Background(), t.TempDir(), filepath.Join(t.TempDir(), "b"), strings.NewReader(""), false); err == nil {
		t.Errorf("a directory that is no pack")
	}
}

// Through the command line: a release of another environment is made where none is, and the directory init runs in
// is left as it was.
func TestInitCommandLine(t *testing.T) {
	pk := generated(t)
	here := t.TempDir()
	t.Chdir(here)
	out := filepath.Join(t.TempDir(), "rel")
	initFlags(t, "", false)
	t.Cleanup(func() { envName = "" })
	said := pack(t, here, "init", pk, out, "--env", "prod", "--target", "memory")
	if !strings.Contains(said, "wrote env.prod.yaml") || !strings.Contains(said, "wrote AGENTS.md") {
		t.Errorf("init said:\n%s", said)
	}
	if _, err := os.Stat(filepath.Join(out, "env.prod.yaml")); err != nil {
		t.Error(err)
	}
	if ents, _ := os.ReadDir(here); len(ents) > 0 {
		t.Errorf("init left %v where it ran", ents)
	}
	if strings.Index(said, "wrote release.yaml") > strings.Index(said, "wrote env.prod.yaml") {
		t.Errorf("not in order:\n%s", said)
	}
}

func TestInitRefusesATargetItDoesNotKnow(t *testing.T) {
	pk := generated(t)
	t.Chdir(t.TempDir())
	initFlags(t, "dokcer", false)
	if _, err := initRelease(context.Background(), pk, filepath.Join(t.TempDir(), "a"), strings.NewReader(""), false); err == nil ||
		!strings.Contains(err.Error(), "--target-source") {
		t.Errorf("a typo: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("#!/bin/sh\n")) }))
	defer srv.Close()
	initSource = srv.URL + "/pack-target-dokcer"
	t.Setenv("PACK_CACHE", t.TempDir())
	out := filepath.Join(t.TempDir(), "b")
	if _, err := initRelease(context.Background(), pk, out, strings.NewReader(""), false); err != nil {
		t.Errorf("a target of another name with its source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("PACK_CACHE"), "bin", "pack-target-dokcer")); err != nil {
		t.Errorf("its program: %v", err)
	}
}

// An image and no program: the target runs from its image; native with only an image is refused.
func TestInitTargetImage(t *testing.T) {
	pk := generated(t)
	t.Chdir(t.TempDir())
	initFlags(t, "ansible", false)
	initImage = "oci://reg.invalid/targets/ansible:1"
	out := filepath.Join(t.TempDir(), "a")
	_, _ = initRelease(context.Background(), pk, out, strings.NewReader(""), false) // the image cannot be pinned here: only the files matter
	raw, err := os.ReadFile(filepath.Join(out, "env.yaml"))
	if err != nil || !strings.Contains(string(raw), "type: docker") {
		t.Errorf("env.yaml: %s %v", raw, err)
	}
	initFlags(t, "ansible", false)
	initImage, initRuntime = "oci://reg.invalid/targets/ansible:1", "native"
	if _, err := initRelease(context.Background(), pk, filepath.Join(t.TempDir(), "b"), strings.NewReader(""), false); err == nil ||
		!strings.Contains(err.Error(), "--target-source") {
		t.Errorf("native with an image only: %v", err)
	}
}
