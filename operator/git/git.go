// Package git is the operator's git (К5): working copies of the projects' repositories, checkouts at a commit,
// commits and pushes of its own, signatures checked against a project's keys. Only the git protocol — no hosting
// APIs, and no git binary: go-git. Credentials are never an argument and never a file that outlives the repo.
package git

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// Repo is a working copy of a project's repository.
type Repo struct {
	r    *git.Repository
	Dir  string
	auth []client.Option
	tmp  string // the repo's own temp files (known_hosts), gone with Close
}

// Clone makes a working copy of url in dir; creds nil — a repository that asks nothing (file://, a public one).
func Clone(ctx context.Context, url, dir string, creds *Credentials) (*Repo, error) {
	tmp, err := os.MkdirTemp("", "pack-operator-git-")
	if err != nil {
		return nil, err
	}
	auth, err := creds.auth(tmp)
	if err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	r, err := git.PlainCloneContext(ctx, dir, &git.CloneOptions{URL: url, ClientOptions: auth})
	if err != nil {
		os.RemoveAll(tmp)
		return nil, fmt.Errorf("clone %s: %w", url, err)
	}
	return &Repo{r: r, Dir: dir, auth: auth, tmp: tmp}, nil
}

// Open is the working copy already in dir.
func Open(dir string, creds *Credentials) (*Repo, error) {
	tmp, err := os.MkdirTemp("", "pack-operator-git-")
	if err != nil {
		return nil, err
	}
	auth, err := creds.auth(tmp)
	if err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	r, err := git.PlainOpen(dir)
	if err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	return &Repo{r: r, Dir: dir, auth: auth, tmp: tmp}, nil
}

// Close gives the repo's temp files up.
func (r *Repo) Close() error {
	if len(r.tmp) > 0 {
		return os.RemoveAll(r.tmp)
	}
	return nil
}

// Fetch brings the remote's refs in.
func (r *Repo) Fetch(ctx context.Context) error {
	err := r.r.FetchContext(ctx, &git.FetchOptions{ClientOptions: r.auth, Prune: true})
	if err == git.NoErrAlreadyUpToDate {
		return nil
	}
	return err
}

// HEAD is the commit the copy is at.
func (r *Repo) HEAD(ctx context.Context) (string, error) {
	ref, err := r.r.Head()
	if err != nil {
		return "", err
	}
	return ref.Hash().String(), nil
}

// Resolve is the sha of a ref (refs/remotes/origin/main, a branch, a sha).
func (r *Repo) Resolve(ctx context.Context, ref string) (string, error) {
	h, err := r.r.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return "", err
	}
	return h.String(), nil
}

// Checkout is the tree of sha written in dest — a copy for reading (a sync run's inputs, a job's checkout),
// never the operator's own work, and with no .git of its own: the files of the commit, nothing else.
// It works for any commit the repo holds, a fetched one among them. Remove it with os.RemoveAll.
func (r *Repo) Checkout(ctx context.Context, sha, dest string) error {
	c, err := r.r.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return fmt.Errorf("checkout %s: %w", sha, err)
	}
	tree, err := c.Tree()
	if err != nil {
		return err
	}
	err = tree.Files().ForEach(func(f *object.File) error {
		p := filepath.Join(dest, f.Name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		mode := f.Mode
		perm := os.FileMode(0o644)
		if mode == filemode.Executable {
			perm = 0o755
		}
		if mode != filemode.Regular && mode != filemode.Executable {
			return fmt.Errorf("checkout %s: %s is %s, not a regular file", sha, f.Name, mode)
		}
		rd, err := f.Reader()
		if err != nil {
			return err
		}
		defer rd.Close()
		w, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, rd); err != nil {
			w.Close()
			return err
		}
		return w.Close()
	})
	if err != nil {
		os.RemoveAll(dest)
		return fmt.Errorf("checkout %s: %w", sha, err)
	}
	return nil
}

// ChangedFiles is the paths that differ between two commits.
func (r *Repo) ChangedFiles(ctx context.Context, a, b string) ([]string, error) {
	ca, err := r.r.CommitObject(plumbing.NewHash(a))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", a, err)
	}
	cb, err := r.r.CommitObject(plumbing.NewHash(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b, err)
	}
	patch, err := ca.PatchContext(ctx, cb)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, s := range patch.Stats() {
		files = append(files, s.Name)
	}
	return files, nil
}

// Log is the commits of ref newest first, each its sha and subject, at most n (0: all).
func (r *Repo) Log(ctx context.Context, ref string, n int) ([][2]string, error) {
	sha, err := r.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	iter, err := r.r.Log(&git.LogOptions{From: plumbing.NewHash(sha)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var commits [][2]string
	err = iter.ForEach(func(c *object.Commit) error {
		if n > 0 && len(commits) >= n {
			return io.EOF
		}
		subject, _, _ := strings.Cut(c.Message, "\n")
		commits = append(commits, [2]string{c.Hash.String(), strings.TrimRight(subject, "\r")})
		return nil
	})
	if err == io.EOF {
		err = nil
	}
	return commits, err
}

// Commit commits the paths of dir (all with add nil) as the operator: author and committer are author
// ("pack-operator <pack-operator@example.org>", К5). It returns the new commit's sha.
func (r *Repo) Commit(ctx context.Context, dir, author, message string, add []string) (string, error) {
	name, email, ok := strings.Cut(author, " <")
	if !ok || !strings.HasSuffix(email, ">") {
		return "", fmt.Errorf("the author is \"Name <address>\": %q", author)
	}
	who := &object.Signature{Name: name, Email: strings.TrimSuffix(email, ">"), When: time.Now()}
	repo := r.r
	if dir != r.Dir {
		var err error
		if repo, err = git.PlainOpen(dir); err != nil {
			return "", err
		}
	}
	w, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	if len(add) == 0 {
		if err := w.AddWithOptions(&git.AddOptions{All: true}); err != nil {
			return "", err
		}
	} else {
		for _, p := range add {
			if _, err := w.Add(p); err != nil {
				return "", err
			}
		}
	}
	h, err := w.Commit(message, &git.CommitOptions{Author: who, Committer: who})
	if err != nil {
		return "", err
	}
	return h.String(), nil
}

// Push pushes refspec (a branch, sha:branch) to origin.
func (r *Repo) Push(ctx context.Context, refspec string) error {
	err := r.r.PushContext(ctx, &git.PushOptions{
		ClientOptions: r.auth,
		RefSpecs:      []config.RefSpec{config.RefSpec(refspec)},
	})
	if err == git.NoErrAlreadyUpToDate {
		return nil
	}
	return err
}

// InitBare is a bare repository at dir — the operator never makes one; tests do.
func InitBare(dir string) error {
	_, err := git.PlainInit(dir, true)
	return err
}
