package sync

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/horchestra/pack/operator/bus"
	opconfig "github.com/horchestra/pack/operator/config"
	"github.com/horchestra/pack/operator/db"
)

// fixture is a project repository: a bare remote and a working seed to commit with, all go-git.
type fixture struct {
	t    *testing.T
	bare string
	seed *git.Repository
	dir  string
}

// newFixture is a repository with a pack in packs/kv and a release in releases/prod/db that names it.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, bare: filepath.Join(root, "repo.git"), dir: filepath.Join(root, "seed")}
	seed, err := git.PlainInit(f.dir, false)
	if err != nil {
		t.Fatal(err)
	}
	f.seed = seed
	f.write("packs/kv/pack.yaml", "name: kv\n")
	f.write("releases/prod/db/release.yaml", "pack: ../../../packs/kv\n")
	f.write("releases/prod/db/values.yaml", "size: 1\n")
	f.commit("the start")
	if _, err := git.PlainInit(f.bare, true); err != nil {
		t.Fatal(err)
	}
	f.push("refs/heads/master:refs/heads/main")
	b, err := git.PlainOpen(f.bare)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) write(name, content string) {
	f.t.Helper()
	p := filepath.Join(f.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// commit commits everything of the seed; the trailer goes into the message when given.
func (f *fixture) commit(message string) string {
	f.t.Helper()
	w, err := f.seed.Worktree()
	if err != nil {
		f.t.Fatal(err)
	}
	if err := w.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		f.t.Fatal(err)
	}
	who := &object.Signature{Name: "alice", Email: "alice@example.org"}
	h, err := w.Commit(message, &git.CommitOptions{Author: who})
	if err != nil {
		f.t.Fatal(err)
	}
	return h.String()
}

func (f *fixture) push(refspec string) {
	f.t.Helper()
	seed, err := git.PlainOpen(f.dir)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := seed.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{f.bare}}); err != nil {
		if _, err2 := seed.Remote("origin"); err2 != nil {
			f.t.Fatalf("the remote: %v, %v", err, err2)
		}
	}
	if err := seed.Push(&git.PushOptions{RefSpecs: []config.RefSpec{config.RefSpec(refspec)}}); err != nil {
		f.t.Fatal(err)
	}
}

// branch pushes the seed's head as a branch of the remote.
func (f *fixture) branch(name string) {
	f.t.Helper()
	f.push("refs/heads/master:refs/heads/" + name)
}

// spy is a Policy that remembers.
type spy struct {
	approvals [][5]string // project, release, digest, sha, by
	merges    [][4]string // project, change, sha, by
}

func (s *spy) Approve(ctx context.Context, project, release, digest, sha, by string) error {
	s.approvals = append(s.approvals, [5]string{project, release, digest, sha, by})
	return nil
}

func (s *spy) Merge(ctx context.Context, project, change, sha, by string) error {
	s.merges = append(s.merges, [4]string{project, change, sha, by})
	return nil
}

// setup is a Syncer on a SQLite database and a collector of its requests.
func setup(t *testing.T, f *fixture, policy Policy, approveKeys []string) (*Syncer, func() []bus.Request) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "operator.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	b := bus.NewSQL(d)
	cfg := &opconfig.Config{Projects: []opconfig.Project{{
		ID: "dev", Repo: "file://" + f.bare, Branch: "main", ApproveKeys: approveKeys}}}
	s := New(cfg, b, d, nil, t.TempDir(), policy)
	t.Cleanup(s.Close)
	group := 0
	collect := func() []bus.Request {
		t.Helper()
		group++ // a new group every time: one group reads every event from the beginning
		sub, err := b.Subscribe(ctx, fmt.Sprintf("test-%s-%d", t.Name(), group), 0)
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		var requests []bus.Request
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			m, err := sub.Next(ctx)
			cancel()
			if err != nil {
				break
			}
			if m.Kind != bus.KindRequest {
				continue
			}
			var req bus.Request
			if err := m.Decode(&req); err != nil {
				t.Fatal(err)
			}
			requests = append(requests, req)
		}
		return requests
	}
	return s, collect
}

// A commit into a release's values asks a plan of that release; a commit into the pack the release names asks
// the same release's plan; a repeated fetch asks nothing more.
func TestSync(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	s, collect := setup(t, f, nil, nil)

	// the first sight: the tree's one release is planned on the head
	if err := s.Sync(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if reqs := collect(); len(reqs) != 1 || reqs[0].Action != bus.ActionPlan ||
		reqs[0].Target.Release != "releases/prod/db" || reqs[0].By != "sync" {
		t.Fatalf("the first sync: %+v", reqs)
	}

	// a commit into the release's values
	f.write("releases/prod/db/values.yaml", "size: 2\n")
	sha := f.commit("bigger")
	f.push("refs/heads/master:refs/heads/main")
	if err := s.Sync(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	reqs := collect()
	if len(reqs) != 2 || reqs[1].SHA != sha || reqs[1].Target.Release != "releases/prod/db" {
		t.Fatalf("a commit into the values: %+v", reqs)
	}

	// a commit into the pack the release names
	f.write("packs/kv/pack.yaml", "name: kv\nversion: 2\n")
	f.commit("pack v2")
	f.push("refs/heads/master:refs/heads/main")
	if err := s.Sync(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if reqs = collect(); len(reqs) != 3 || reqs[2].Target.Release != "releases/prod/db" {
		t.Fatalf("a commit into the pack: %+v", reqs)
	}

	// a commit that touches nothing of a release
	f.write("docs/readme.md", "words\n")
	f.commit("words")
	f.push("refs/heads/master:refs/heads/main")
	if err := s.Sync(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if reqs = collect(); len(reqs) != 3 {
		t.Fatalf("an unrelated commit: %+v", reqs)
	}

	// a repeated fetch: nothing new
	if err := s.Sync(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if reqs = collect(); len(reqs) != 3 {
		t.Fatalf("a repeated fetch: %+v", reqs)
	}

	// a change-request branch: its commits plan too
	f.write("releases/prod/db/values.yaml", "size: 3\n")
	crSHA := f.commit("the change")
	f.branch("cr-resize")
	if err := s.Sync(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if reqs = collect(); len(reqs) != 4 || reqs[3].SHA != crSHA {
		t.Fatalf("a change-request branch: %+v", reqs)
	}
}

// The trailers: a signed Pack-Approve on the main branch and a signed Pack-Merge on a change-request branch
// reach the policy; an unsigned trailer is ignored.
func TestTrailers(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git, for the signing fixture")
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen, for the signing fixture")
	}
	ctx := context.Background()
	f := newFixture(t)
	keys := t.TempDir()
	if out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", filepath.Join(keys, "own")).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %s", out)
	}
	pub, err := os.ReadFile(filepath.Join(keys, "own.pub"))
	if err != nil {
		t.Fatal(err)
	}
	gitCfg := func(args ...string) {
		t.Helper()
		if out, err := exec.Command(gitBin, append([]string{"-C", f.dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	gitCfg("config", "gpg.format", "ssh")
	gitCfg("config", "user.signingkey", filepath.Join(keys, "own"))
	gitCfg("config", "user.name", "alice")
	gitCfg("config", "user.email", "alice@example.org")

	policy := &spy{}
	s, _ := setup(t, f, policy, []string{strings.TrimSpace(string(pub))})

	// an unsigned trailer: ignored
	f.write("releases/prod/db/values.yaml", "size: 2\n")
	f.commit("bigger\n\nPack-Approve: releases/prod/db sha256:aaa")
	f.push("refs/heads/master:refs/heads/main")
	if err := s.Sync(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if len(policy.approvals) > 0 {
		t.Fatalf("an unsigned trailer approved: %+v", policy.approvals)
	}

	// a signed Pack-Approve on the main branch
	f.write("releases/prod/db/values.yaml", "size: 3\n")
	w, err := f.seed.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(gitBin, "-C", f.dir, "commit", "--quiet", "-S",
		"-m", "resize\n\nPack-Approve: releases/prod/db sha256:bbb").CombinedOutput(); err != nil {
		t.Fatalf("the signed commit: %s", out)
	}
	f.push("refs/heads/master:refs/heads/main")
	if err := s.Sync(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if len(policy.approvals) != 1 || policy.approvals[0][1] != "releases/prod/db" ||
		policy.approvals[0][2] != "sha256:bbb" || policy.approvals[0][4] != "alice <alice@example.org>" {
		t.Fatalf("the signed trailer: %+v", policy.approvals)
	}

	// a signed Pack-Merge on a change-request branch's head
	f.write("releases/prod/db/values.yaml", "size: 4\n")
	if err := w.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(gitBin, "-C", f.dir, "commit", "--quiet", "-S",
		"-m", "the change\n\nPack-Merge: cr-42").CombinedOutput(); err != nil {
		t.Fatalf("the merge commit: %s", out)
	}
	f.branch("cr-42")
	if err := s.Sync(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if len(policy.merges) != 1 || policy.merges[0][1] != "cr-42" {
		t.Fatalf("the merge trailer: %+v", policy.merges)
	}
}
