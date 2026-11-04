package git

import (
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// makeBare is a bare repository with one commit on main, made with go-git itself.
func makeBare(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	seedDir := filepath.Join(root, "seed")
	seed, err := git.PlainInit(seedDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedDir, "README"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := seed.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("README"); err != nil {
		t.Fatal(err)
	}
	who := &object.Signature{Name: "t", Email: "t@t"}
	if _, err := w.Commit("one", &git.CommitOptions{Author: who}); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(root, "repo.git")
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{bare}}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Push(&git.PushOptions{
		RefSpecs: []config.RefSpec{"refs/heads/master:refs/heads/main"}}); err != nil {
		t.Fatal(err)
	}
	// the bare's HEAD is main: what a clone checks out
	b, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")); err != nil {
		t.Fatal(err)
	}
	return bare
}

// clone, commit, push, fetch: the operator's moves against file:// (no credentials).
func TestWorkflow(t *testing.T) {
	ctx := context.Background()
	bare := makeBare(t)
	r, err := Clone(ctx, "file://"+bare, filepath.Join(t.TempDir(), "work"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	head, err := r.HEAD(ctx)
	if err != nil || len(head) != 40 {
		t.Fatalf("HEAD: %q, %v", head, err)
	}
	// another copy, cloned before the push, sees it only after fetch
	other, err := Clone(ctx, "file://"+bare, filepath.Join(t.TempDir(), "other"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := os.WriteFile(filepath.Join(r.Dir, "release.yaml"), []byte("pack: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := r.Commit(ctx, r.Dir, "pack-operator <pack-operator@example.org>", "a release", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Push(ctx, "refs/heads/main:refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	old, err := other.Resolve(ctx, "refs/remotes/origin/main")
	if err != nil || old != head {
		t.Fatalf("before the fetch, origin/main is the old: %q, %v — want %s", old, err, head)
	}
	if err := other.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	remote, err := other.Resolve(ctx, "refs/remotes/origin/main")
	if err != nil || remote != sha {
		t.Fatalf("origin/main: %q, %v — want %s", remote, err, sha)
	}
	// a checkout at the sha holds the file
	dest := filepath.Join(t.TempDir(), "wt")
	if err := other.Checkout(ctx, sha, dest); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "release.yaml")); err != nil || string(b) != "pack: x\n" {
		t.Errorf("the checkout at %s: %q, %v", sha, b, err)
	}
	changed, err := other.ChangedFiles(ctx, head, sha)
	if err != nil || len(changed) != 1 || changed[0] != "release.yaml" {
		t.Errorf("the changes %s…%s: %v, %v", head, sha, changed, err)
	}
	log, err := other.Log(ctx, "refs/remotes/origin/main", 10)
	if err != nil || len(log) != 2 || log[0][0] != sha || log[0][1] != "a release" {
		t.Errorf("the log: %v, %v", log, err)
	}
	// the committer is the operator
	c, err := other.r.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		t.Fatal(err)
	}
	if c.Author.Name != "pack-operator" || c.Author.Email != "pack-operator@example.org" ||
		c.Committer.Name != "pack-operator" || c.Committer.Email != "pack-operator@example.org" {
		t.Errorf("author and committer: %+v, %+v", c.Author, c.Committer)
	}
}

// git over HTTP, credentials a basic-auth token: the right one clones and pushes, the wrong one is refused.
// The server side is git's own http-backend (the remote is a fixture; the operator's side is go-git).
func TestHTTPS(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git, for the fixture server")
	}
	bare := makeBare(t)
	if out, err := exec.Command(gitBin, "-C", bare, "config", "http.receivepack", "true").CombinedOutput(); err != nil {
		t.Fatalf("config: %s", out)
	}
	root := filepath.Dir(bare)
	cgiHandler := &cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "operator" || pass != "t0ken" {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		cgiHandler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	ctx := context.Background()
	creds := &Credentials{Username: "operator", Token: "t0ken"}
	r, err := Clone(ctx, srv.URL+"/repo.git", filepath.Join(t.TempDir(), "work"), creds)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := os.WriteFile(filepath.Join(r.Dir, "pushed"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(ctx, r.Dir, "pack-operator <pack-operator@example.org>", "over http", nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(ctx, "refs/heads/main:refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]*Credentials{
		"a wrong token": {Username: "operator", Token: "wrong"},
		"no token":      nil,
	} {
		cl, err := Clone(ctx, srv.URL+"/repo.git", filepath.Join(t.TempDir(), "spy"), c)
		if err == nil {
			cl.Close()
			t.Errorf("%s cloned", name)
		}
	}
	if b, err := os.ReadFile(filepath.Join(bare, "HEAD")); err == nil && !strings.Contains(string(b), "main") {
		t.Errorf("the bare's HEAD: %s", b)
	}
}
