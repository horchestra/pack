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

// CommitInfo is what a sync run needs of a commit: its message (the trailers live there), its author, its parents.
type CommitInfo struct {
	SHA     string
	Message string
	Author  string // "Name <email>"
	Parents []string
}

// Inspect reads a commit the repo holds.
func (r *Repo) Inspect(ctx context.Context, sha string) (CommitInfo, error) {
	c, err := r.r.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return CommitInfo{}, fmt.Errorf("%s: %w", sha, err)
	}
	info := CommitInfo{SHA: sha, Message: c.Message,
		Author: fmt.Sprintf("%s <%s>", c.Author.Name, c.Author.Email)}
	for _, p := range c.ParentHashes {
		info.Parents = append(info.Parents, p.String())
	}
	return info, nil
}

// MergeBase is the best common ancestor of two commits, "" when there is none.
func (r *Repo) MergeBase(ctx context.Context, a, b string) (string, error) {
	ca, err := r.r.CommitObject(plumbing.NewHash(a))
	if err != nil {
		return "", fmt.Errorf("%s: %w", a, err)
	}
	cb, err := r.r.CommitObject(plumbing.NewHash(b))
	if err != nil {
		return "", fmt.Errorf("%s: %w", b, err)
	}
	bases, err := ca.MergeBase(cb)
	if err != nil {
		return "", err
	}
	if len(bases) == 0 {
		return "", nil
	}
	return bases[0].Hash.String(), nil
}

// RemoteBranches are the branches fetch brought (refs/remotes/origin/*): each its name without "origin/" and
// its sha; the remote's own HEAD is not one of them.
func (r *Repo) RemoteBranches(ctx context.Context) ([][2]string, error) {
	refs, err := r.r.References()
	if err != nil {
		return nil, err
	}
	var branches [][2]string
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().String()
		rest, ok := strings.CutPrefix(name, "refs/remotes/origin/")
		if !ok || rest == "HEAD" {
			return nil
		}
		branches = append(branches, [2]string{rest, ref.Hash().String()})
		return nil
	})
	return branches, err
}

// ErrMergeConflict is a merge whose branches both changed a file: a person rebases the change request.
var ErrMergeConflict = fmt.Errorf("the branches changed the same files")

// Merge merges sha into the remote-tracking branch (refs/remotes/origin/<into>) and answers the commit the
// branch should point at, and whether a merge commit was made for it: sha itself when the branch fast-forwards,
// a new two-parent commit when the branches diverged and changed no file in common (its tree is sha's);
// into's own head, false, when sha is merged already; ErrMergeConflict when both changed a file. Push moves
// the branch; the answer is what to push.
func (r *Repo) Merge(ctx context.Context, into, sha, author, message string) (string, bool, error) {
	head, err := r.Resolve(ctx, "refs/remotes/origin/"+into)
	if err != nil {
		return "", false, err
	}
	base, err := r.MergeBase(ctx, head, sha)
	if err != nil {
		return "", false, err
	}
	switch base {
	case sha:
		return head, false, nil // merged already
	case head:
		return sha, false, nil // a fast-forward
	}
	mine, err := r.ChangedFiles(ctx, base, head)
	if err != nil {
		return "", false, err
	}
	theirs, err := r.ChangedFiles(ctx, base, sha)
	if err != nil {
		return "", false, err
	}
	both := map[string]bool{}
	for _, f := range mine {
		both[f] = true
	}
	for _, f := range theirs {
		if both[f] {
			return "", false, fmt.Errorf("%s: %w", f, ErrMergeConflict)
		}
	}
	c, err := r.r.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return "", false, err
	}
	name, email, ok := strings.Cut(author, " <")
	if !ok || !strings.HasSuffix(email, ">") {
		return "", false, fmt.Errorf("the author is \"Name <address>\": %q", author)
	}
	who := object.Signature{Name: name, Email: strings.TrimSuffix(email, ">"), When: time.Now()}
	commit := object.Commit{
		Author:       who,
		Committer:    who,
		Message:      message,
		TreeHash:     c.TreeHash,
		ParentHashes: []plumbing.Hash{plumbing.NewHash(head), plumbing.NewHash(sha)},
	}
	obj := r.r.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return "", false, err
	}
	h, err := r.r.Storer.SetEncodedObject(obj)
	if err != nil {
		return "", false, err
	}
	return h.String(), true, nil
}

// HasRemoteBranch says whether origin has the branch.
func (r *Repo) HasRemoteBranch(ctx context.Context, branch string) (bool, error) {
	_, err := r.r.Reference(plumbing.NewRemoteReferenceName("origin", branch), true)
	if err == plumbing.ErrReferenceNotFound {
		return false, nil
	}
	return err == nil, err
}
