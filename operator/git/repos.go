package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Repos is a process's working copies: one per repository, cloned on first use, reused after — the worker's
// source of checkouts (B6).
type Repos struct {
	dir   string
	creds Store
	mu    sync.Mutex
	repos map[string]*Repo
}

// NewRepos keeps working copies under dir; creds resolves the projects' credential references (nil — none).
func NewRepos(dir string, creds Store) *Repos {
	return &Repos{dir: dir, creds: creds, repos: map[string]*Repo{}}
}

// Get is the working copy of the repository id (url, credsRef for the first clone).
func (rs *Repos) Get(ctx context.Context, id, url, credsRef string) (*Repo, error) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.get(ctx, id, url, credsRef)
}

// get is Get without the lock.
func (rs *Repos) get(ctx context.Context, id, url, credsRef string) (*Repo, error) {
	if r, ok := rs.repos[id]; ok {
		return r, nil
	}
	var creds *Credentials
	var err error
	if len(credsRef) > 0 && rs.creds != nil {
		if creds, err = rs.creds.Credentials(ctx, credsRef); err != nil {
			return nil, fmt.Errorf("credentials %s: %w", credsRef, err)
		}
	}
	dir := filepath.Join(rs.dir, id)
	var r *Repo
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if r, err = Open(dir, creds); err != nil {
			return nil, err
		}
	} else {
		if err := os.MkdirAll(rs.dir, 0o755); err != nil {
			return nil, err
		}
		if r, err = Clone(ctx, url, dir, creds); err != nil {
			return nil, err
		}
	}
	rs.repos[id] = r
	return r, nil
}

// Checkout fetches and writes the tree of sha of the repository id in dest.
func (rs *Repos) Checkout(ctx context.Context, id, url, credsRef, sha, dest string) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	r, err := rs.get(ctx, id, url, credsRef)
	if err != nil {
		return err
	}
	if err := r.Fetch(ctx); err != nil {
		return err
	}
	return r.Checkout(ctx, sha, dest)
}

// Close gives the working copies' temp files up.
func (rs *Repos) Close() {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, r := range rs.repos {
		_ = r.Close()
	}
}
