package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/horchestra/pack/sdk"
	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/testpacks/openbao"
	"github.com/horchestra/pack/internal/testpacks/regserver"
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/environment"
	"github.com/horchestra/pack/pkg/render"
)

// pack runs the command line in dir and returns what it printed. A command that fails ends the process (see run),
// so these are the commands that are meant to succeed.
func pack(t *testing.T, dir string, args ...string) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	rootCmd.SetArgs(append([]string{"-C", dir}, args...))
	err = rootCmd.Execute()
	os.Stdout = stdout
	_ = w.Close()
	out := <-done
	if err != nil {
		t.Fatalf("pack %v: %v\n%s", args, err, out)
	}
	return out
}

func write(t *testing.T, dir, name string, v any) {
	t.Helper()
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A release on a directory pack: the openbao pack with the postgres it holds.
func releaseOf(t *testing.T, packRef string, env map[string]any) string {
	t.Helper()
	d := t.TempDir()
	write(t, d, "release.yaml", map[string]any{"pack": packRef})
	write(t, d, "values.yaml", map[string]any{
		"openbao":  map[string]any{"api_addr": "https://bao:8200"},
		"postgres": map[string]any{"id": "n1"},
	})
	base := map[string]any{
		"target": "memory",
		"keys":   map[string]any{"postgres": map[string]any{"resources": map[string]any{"requests": map[string]any{"memory": "2Gi"}}}},
	}
	maps.Copy(base, env)
	write(t, d, "env.yaml", base)
	return d
}

func generated(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "packs")
	if err := sdk.Generate(p, &openbao.OpenBao{}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(p, "openbao")
}

func TestGraph(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	graphFormat = "dot"
	dot := pack(t, d, "graph")
	for _, want := range []string{`"postgres" -> "openbao";`, `"savepoint:postgres" [shape=diamond];`} {
		if !strings.Contains(dot, want) {
			t.Errorf("dot lacks %s:\n%s", want, dot)
		}
	}
	mer := pack(t, d, "graph", "-f", "mermaid")
	if !strings.HasPrefix(mer, "flowchart LR") {
		t.Errorf("mermaid:\n%s", mer)
	}
	graphFormat = "dot"
}

func TestTemplate(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	templateKey, templateOut = "", ""
	t.Cleanup(func() { templateKey, templateOut = "", "" })

	out := pack(t, d, "template", "--key", "openbao")
	if !strings.Contains(out, "# openbao: /etc/openbao/config.json") || !strings.Contains(out, `"api_addr": "https://bao:8200"`) ||
		strings.Contains(out, "tembo.env") {
		t.Fatalf("template --key openbao:\n%s", out)
	}
	templateKey = "" // a flag keeps its value between runs in one process
	to := filepath.Join(t.TempDir(), "out")
	pack(t, d, "template", "-o", to)
	var got []string
	_ = filepath.WalkDir(to, func(p string, e os.DirEntry, _ error) error {
		if !e.IsDir() {
			rel, _ := filepath.Rel(to, p)
			got = append(got, filepath.ToSlash(rel))
		}
		return nil
	})
	want := []string{"openbao/etc/openbao/config.json", "postgres/etc/tembo/tembo.env"}
	if !slices.Equal(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
}

func TestEmitUnknownKey(t *testing.T) {
	if err := emit(&render.Release{}, "nope", ""); err == nil {
		t.Fatal("a key the release has not is an error")
	}
}

// The journal keeps what run.journal_keep says, and `log --prune` takes the rest.
func TestLogPrune(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	pack(t, d, "apply")
	pack(t, d, "apply") // nothing changes, but it is a saga of its own
	if runs := strings.Fields(pack(t, d, "log", "--list")); len(runs) == 0 {
		t.Fatal("apply keeps a journal")
	}
	if out := pack(t, d, "log", "--prune", "0"); !strings.Contains(out, "removed") {
		t.Fatalf("prune: %s", out)
	}
	_ = logCmd.Flags().Set("prune", "-1") // a flag keeps its value between runs in one process
	if runs := strings.Fields(pack(t, d, "log", "--list")); len(runs) != 0 {
		t.Fatalf("left: %v", runs)
	}
}

// A pack in a registry: init writes the lock and brings the pack, and the commands that follow read it from there.
func TestRefreshFromARegistry(t *testing.T) {
	host := regserver.Serve(t, &openbao.OpenBao{})
	ref := "oci://" + host + "/packs/openbao:2.1.0"
	d := releaseOf(t, ref, nil)
	write(t, d, "release.yaml", map[string]any{"pack": ref, "registry": map[string]any{"plain_http": true}})

	out := pack(t, d, "refresh")
	if !strings.Contains(out, "locked at sha256:") {
		t.Fatalf("init: %s", out)
	}
	lock, err := release.ReadLock(d)
	if err != nil || lock == nil || lock.Pack != ref || !strings.HasPrefix(lock.Digest, "sha256:") {
		t.Fatalf("lock = %+v, %v", lock, err)
	}
	if ents, _ := os.ReadDir(filepath.Join(d, ".pack", "packs")); len(ents) == 0 {
		t.Fatal("the pack is not in .pack/packs")
	}
	// again: the same lock, nothing new
	pack(t, d, "refresh")
	if again, _ := release.ReadLock(d); again == nil || !reflect.DeepEqual(again, lock) {
		t.Fatalf("lock changed: %+v", again)
	}
	// and what follows runs from the pack directory
	graphFormat = "dot"
	if dot := pack(t, d, "graph"); !strings.Contains(dot, `"postgres" -> "openbao";`) {
		t.Fatalf("graph:\n%s", dot)
	}
}

// `state: target` keeps its id in release.yaml; refresh makes it once.
func TestRefreshMakesTheIDOfAStateKeptInTheTarget(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	write(t, d, "release.yaml", map[string]any{"pack": generated(t), "state": "target"})
	pack(t, d, "refresh")
	rel, err := release.Load(d, "")
	if err != nil || len(rel.ID) == 0 {
		t.Fatalf("release = %+v, %v", rel, err)
	}
	id := rel.ID
	pack(t, d, "refresh")
	if rel, _ := release.Load(d, ""); rel.ID != id {
		t.Fatalf("the id changed: %s → %s", id, rel.ID)
	}
}

// The programs of the targets are downloaded by refresh from the sources release.yaml names, into .pack/bin; the lock
// keeps their checksums; a source that gives something else is refused until --upgrade.
func TestRefreshBringsTheTargets(t *testing.T) {
	body := []byte("#!/bin/sh\necho one\n")
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	packDir := generated(t)
	d := releaseOf(t, packDir, map[string]any{"target": "fake"})
	write(t, d, "release.yaml", map[string]any{
		"pack":    packDir,
		"targets": map[string]any{"fake": srv.URL + "/pack-target-fake_{os}_{arch}"},
	})
	out := pack(t, d, "refresh")
	file := filepath.Join(d, ".pack", "bin", "pack-target-fake")
	if got, err := os.ReadFile(file); err != nil || string(got) != string(body) {
		t.Fatalf("the program: %q %v\n%s", got, err, out)
	}
	if fi, _ := os.Stat(file); fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("not executable: %v", fi.Mode())
	}
	sum := sha256.Sum256(body)
	lock, err := release.ReadLock(d)
	if err != nil || lock.Targets["fake"].SHA256 != hex.EncodeToString(sum[:]) || !strings.Contains(lock.Targets["fake"].Source, "{os}") {
		t.Fatalf("lock = %+v, %v", lock, err)
	}
	if !strings.Contains(out, "downloaded") {
		t.Errorf("init: %s", out)
	}

	// again: the file is there with the locked checksum, nothing is fetched
	if out := pack(t, d, "refresh"); !strings.Contains(out, "up to date") {
		t.Errorf("again: %s", out)
	}

	// the source gives something else: refused (a failing command exits, so the step is called directly)
	mu.Lock()
	body = []byte("#!/bin/sh\necho two\n")
	mu.Unlock()
	_ = os.Remove(file)
	rel, err := release.Load(d, "")
	if err != nil {
		t.Fatal(err)
	}
	env, err := environment.Load(context.Background(), rel.Config(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bringTargets(context.Background(), workdir.New(rel, ""), env, lock, false); err == nil || !strings.Contains(err.Error(), "--upgrade") {
		t.Fatalf("a program that is not the locked one: %v", err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("the refused program is removed")
	}
	// --upgrade takes it
	pack(t, d, "refresh", "--upgrade")
	refreshUpgrade = false // a flag keeps its value between runs in one process
	if got, _ := os.ReadFile(file); string(got) != string(body) {
		t.Errorf("after --upgrade: %q", got)
	}

	// a target with no source and no program is said so
	d2 := releaseOf(t, packDir, map[string]any{"target": "nowhere"})
	rel2, _ := release.Load(d2, "")
	env2, _ := environment.Load(context.Background(), rel2.Config(), nil)
	if err := bringTargets(context.Background(), workdir.New(rel2, ""), env2, &release.Lock{}, false); err == nil || !strings.Contains(err.Error(), "no source") {
		t.Errorf("no source, no program: %v", err)
	}
}
