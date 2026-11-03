package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/workdir"
)

// inRelease sets the run up in d the way the root command does, for a command whose failure is to be seen: run
// ends the process on one.
func inRelease(t *testing.T, d string) {
	t.Helper()
	t.Chdir(d)
	rel, err := release.Load(".", "")
	if err != nil {
		t.Fatal(err)
	}
	dir = workdir.New(rel, "")
	dir.Ensure()
}

func valuesFlags(t *testing.T, rewrite, check bool) {
	t.Helper()
	valuesRewrite, valuesCheck, valuesLang = rewrite, check, ""
	t.Cleanup(func() { valuesRewrite, valuesCheck, valuesLang = false, false, "" })
}

func TestValuesWrite(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	valuesFlags(t, false, false)
	if out := pack(t, d, "values", "write", "--lang", "ru"); !strings.HasPrefix(out, "wrote ") {
		t.Fatalf("write: %s", out)
	}
	text, err := os.ReadFile(filepath.Join(d, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# pack values: sha256:", "  api_addr: https://bao:8200\n", "  id: n1\n", "  #log_level: info\n",
		"  # Насколько подробно пишет сервер.\n"} { // the description in ru
		if !strings.Contains(string(text), want) {
			t.Errorf("values.yaml lacks %q:\n%s", want, text)
		}
	}
	if out := pack(t, d, "values", "write", "--lang", "ru"); !strings.Contains(out, "up to date") {
		t.Errorf("again: %s", out)
	}
	valuesFlags(t, false, true)
	if out := pack(t, d, "values", "write", "--lang", "ru", "--check"); !strings.Contains(out, "ok") {
		t.Errorf("--check: %s", out)
	}
}

func TestValuesWriteRefuses(t *testing.T) {
	ctx := context.Background()
	d := releaseOf(t, generated(t), nil)
	inRelease(t, d)
	valuesFlags(t, false, false)
	if _, err := writeValues(ctx); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d, "values.yaml")
	text, _ := os.ReadFile(path)
	noted := strings.Replace(string(text), "  api_addr: https://bao:8200\n", "  # ours\n  api_addr: https://bao:8200\n", 1)
	if err := os.WriteFile(path, []byte(noted), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeValues(ctx); err == nil || !strings.Contains(err.Error(), "--rewrite") {
		t.Errorf("a note of the operator's: %v", err)
	}
	valuesFlags(t, false, true)
	if _, err := writeValues(ctx); err == nil || !strings.Contains(err.Error(), "not what pack would write") {
		t.Errorf("--check of an edited file: %v", err)
	}
	valuesFlags(t, true, false)
	if _, err := writeValues(ctx); err != nil {
		t.Errorf("--rewrite: %v", err)
	}
	if back, _ := os.ReadFile(path); strings.Contains(string(back), "# ours") {
		t.Errorf("--rewrite kept the note")
	}

	valuesFlags(t, false, false)
	if err := os.WriteFile(path, []byte("values:\n  openbao:\n    api_addr: x\nskip:\n  - postgres\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeValues(ctx); err == nil || !strings.Contains(err.Error(), "whole document") {
		t.Errorf("a whole document: %v", err)
	}
	if err := os.WriteFile(filepath.Join(d, "more.values.yaml"), []byte("postgres:\n  id: n2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inRelease(t, d)
	if _, err := writeValues(ctx); err == nil || !strings.Contains(err.Error(), "values.yaml alone") {
		t.Errorf("values in two files: %v", err)
	}
}

// values: [values.yaml] in release.yaml with no file yet: the file is written.
func TestValuesWriteMakesTheFile(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	if err := os.Remove(filepath.Join(d, "values.yaml")); err != nil {
		t.Fatal(err)
	}
	write(t, d, "release.yaml", map[string]any{"pack": generated(t), "values": []string{"values.yaml"}})
	inRelease(t, d)
	valuesFlags(t, false, false)
	if out, err := writeValues(context.Background()); err != nil || !strings.HasPrefix(out, "wrote ") {
		t.Fatalf("%q, %v", out, err)
	}
}

// values write records no sources where nothing gave any: no file of them.
func TestValuesWriteMakesNoSources(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	inRelease(t, d)
	valuesFlags(t, false, false)
	if _, err := writeValues(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, ".pack", "values.sources.json")); err == nil {
		t.Error("values write made a file of sources")
	}
}
