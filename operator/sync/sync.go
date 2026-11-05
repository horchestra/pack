// Package sync is the operator's synchronizer (B4): it fetches every project's repository — on the interval of
// git.fetch_interval and when POST /api/sync asks — diffs the known and the new HEAD of the main branch and of
// every change-request branch, and turns what changed into records on the Bus: a plan request for every new
// commit on every release it touches (the release's own directory, or the directory its pack: names), and the
// trailers of signed commits — Pack-Approve, Pack-Merge (К5) — into approvals and merges through the policy (B5).
package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/pkg/logging"

	"github.com/horchestra/pack/operator/bus"
	"github.com/horchestra/pack/operator/config"
	"github.com/horchestra/pack/operator/db"
	"github.com/horchestra/pack/operator/git"
)

// The trailers of К5.
const (
	trailerApprove = "Pack-Approve:"
	trailerMerge   = "Pack-Merge:"
)

// Policy is what the trailers of signed commits ask (B5): an approval of a release at a sha and digest,
// the merge of a change request. A nil Policy means trailers are only noticed (the audit is the log).
type Policy interface {
	Approve(ctx context.Context, project, release, digest, sha, by string) error
	Merge(ctx context.Context, project, change, sha, by string) error
}

// Syncer keeps the working copies and the known refs, and syncs.
type Syncer struct {
	cfg    *config.Config
	bus    bus.Bus
	db     *db.DB
	creds  git.Store
	dir    string // the working copies: dir/<project>
	policy Policy
	log    *zerolog.Logger

	mu    sync.Mutex // one sync at a time: the interval and POST /api/sync never overlap
	repos map[string]*git.Repo
}

// New is a synchronizer; dir is where the working copies live.
func New(cfg *config.Config, b bus.Bus, d *db.DB, creds git.Store, dir string, policy Policy) *Syncer {
	return &Syncer{cfg: cfg, bus: b, db: d, creds: creds, dir: dir, policy: policy,
		log: logging.For("sync"), repos: map[string]*git.Repo{}}
}

// Close gives the working copies up.
func (s *Syncer) Close() {
	for _, r := range s.repos {
		_ = r.Close()
	}
}

// Run syncs every project on the interval until ctx ends; only the leader of "sync" works, so several
// operators may run. A failed sync is logged and retried on the next tick.
func (s *Syncer) Run(ctx context.Context) error {
	interval := s.cfg.Git.FetchInterval.Duration
	if interval <= 0 {
		interval = config.DefaultFetchInterval
	}
	ttl := 2 * interval
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if ok, err := s.bus.Leader(ctx, "sync", ttl); err != nil {
			s.log.Warn().Err(err).Msg("the leadership")
		} else if ok {
			for _, p := range s.cfg.Projects {
				if err := s.Sync(ctx, p.ID); err != nil {
					s.log.Warn().Err(err).Str("project", p.ID).Msg("the sync")
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// Sync fetches one project now and processes what the fetch brought. Safe to call any time — the interval's
// own runs wait for it.
func (s *Syncer) Sync(ctx context.Context, projectID string) error {
	var p *config.Project
	for i := range s.cfg.Projects {
		if s.cfg.Projects[i].ID == projectID {
			p = &s.cfg.Projects[i]
			break
		}
	}
	if p == nil {
		return fmt.Errorf("no project %s", projectID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.repo(ctx, p)
	if err != nil {
		return err
	}
	if err := r.Fetch(ctx); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if err := s.syncBranch(ctx, p, r, p.Branch); err != nil {
		return err
	}
	// the change-request branches: every remote branch but the main one
	branches, err := r.RemoteBranches(ctx)
	if err != nil {
		return err
	}
	for _, br := range branches {
		if br[0] == p.Branch {
			continue
		}
		if err := s.syncBranch(ctx, p, r, br[0]); err != nil {
			s.log.Warn().Err(err).Str("project", p.ID).Str("branch", br[0]).Msg("the change-request branch")
		}
	}
	return nil
}

// Checkout writes the tree of sha of the project's repository in dest — the policy reads a release's env
// there (policy.Git). Remove it with os.RemoveAll.
func (s *Syncer) Checkout(ctx context.Context, projectID, sha, dest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cfg.Projects {
		if s.cfg.Projects[i].ID == projectID {
			r, err := s.repo(ctx, &s.cfg.Projects[i])
			if err != nil {
				return err
			}
			return r.Checkout(ctx, sha, dest)
		}
	}
	return fmt.Errorf("no project %s", projectID)
}

// Head is the last sha the synchronizer processed on the project's branch (empty before the first sync).
func (s *Syncer) Head(ctx context.Context, projectID string) (string, error) {
	for _, p := range s.cfg.Projects {
		if p.ID == projectID {
			branch := p.Branch
			if len(branch) == 0 {
				branch = "main"
			}
			return s.knownRef(ctx, projectID, branch)
		}
	}
	return "", fmt.Errorf("no project %s", projectID)
}

// repo is the project's working copy, cloned on first use.
func (s *Syncer) repo(ctx context.Context, p *config.Project) (*git.Repo, error) {
	if r, ok := s.repos[p.ID]; ok {
		return r, nil
	}
	dir := filepath.Join(s.dir, p.ID)
	var r *git.Repo
	var creds *git.Credentials
	if len(p.Credentials) > 0 && s.creds != nil {
		var err error
		if creds, err = s.creds.Credentials(ctx, p.Credentials); err != nil {
			return nil, fmt.Errorf("credentials %s: %w", p.Credentials, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if r, err = git.Open(dir, creds); err != nil {
			return nil, err
		}
	} else {
		if err := os.MkdirAll(s.dir, 0o755); err != nil {
			return nil, err
		}
		if r, err = git.Clone(ctx, p.Repo, dir, creds); err != nil {
			return nil, err
		}
	}
	s.repos[p.ID] = r
	return r, nil
}

// syncBranch processes the new commits of one branch: plan requests on the affected releases, and the trailers
// the branch's commits carry.
func (s *Syncer) syncBranch(ctx context.Context, p *config.Project, r *git.Repo, branch string) error {
	head, err := r.Resolve(ctx, "refs/remotes/origin/"+branch)
	if err != nil {
		return fmt.Errorf("the branch %s: %w", branch, err)
	}
	known, err := s.knownRef(ctx, p.ID, branch)
	if err != nil {
		return err
	}
	if head == known {
		return nil // nothing new: a repeated fetch makes no new requests
	}
	main := branch == p.Branch
	if !main {
		// the change request of the branch points at what was planned last (B9)
		_, _ = s.db.ExecContext(ctx, s.bind(`update operator_changes set sha = ?, updated_at = current_timestamp
			where project = ? and branch = ? and state = 'open'`), head, p.ID, branch)
	}
	commits, err := s.newCommits(ctx, r, head, known, main)
	if err != nil {
		return err
	}
	for i := len(commits) - 1; i >= 0; i-- { // the oldest first, so the newest plan stands last
		if err := s.commit(ctx, p, r, branch, commits[i], main); err != nil {
			return err
		}
	}
	return s.setRef(ctx, p.ID, branch, head)
}

// newCommits are the commits of head back to known (newest first). On the first sight of a branch there is
// nothing to report: only the head, so its trailers register and its ref is recorded.
func (s *Syncer) newCommits(ctx context.Context, r *git.Repo, head, known string, main bool) ([]git.CommitInfo, error) {
	if len(known) == 0 {
		info, err := r.Inspect(ctx, head)
		if err != nil {
			return nil, err
		}
		return []git.CommitInfo{info}, nil
	}
	var commits []git.CommitInfo
	for sha := head; sha != known && len(sha) > 0 && len(commits) < 200; {
		info, err := r.Inspect(ctx, sha)
		if err != nil {
			return nil, err
		}
		commits = append(commits, info)
		if len(info.Parents) == 0 {
			break
		}
		sha = info.Parents[0] // first parent: the branch's own line, not what a merge brought in
	}
	return commits, nil
}

// commit is one new commit: a plan request per affected release, and the trailers it carries.
func (s *Syncer) commit(ctx context.Context, p *config.Project, r *git.Repo, branch string, c git.CommitInfo, main bool) error {
	var changed []string
	if len(c.Parents) == 0 {
		changed = nil // the root commit: everything in it is new — every release is affected
	} else {
		var err error
		if changed, err = r.ChangedFiles(ctx, c.Parents[0], c.SHA); err != nil {
			return fmt.Errorf("the changes of %s: %w", c.SHA, err)
		}
	}
	affected, err := s.affected(ctx, r, c.SHA, changed)
	if err != nil {
		return err
	}
	for _, rel := range affected {
		req, err := bus.NewRequest(p.ID, c.SHA, bus.Target{Release: rel}, bus.ActionPlan, bus.Params{}, "sync")
		if err != nil {
			return err
		}
		if _, created, err := s.bus.Submit(ctx, req); err != nil {
			return err
		} else if created {
			s.log.Info().Str("project", p.ID).Str("branch", branch).Str("sha", c.SHA).Str("release", rel).
				Msg("a plan is requested")
		}
	}
	return s.trailers(ctx, p, r, c, main)
}

// affected is the releases of the tree at sha a change of paths touches: a release is touched when a path is in
// its directory or in the directory its pack: names.
func (s *Syncer) affected(ctx context.Context, r *git.Repo, sha string, changed []string) ([]string, error) {
	if changed != nil && len(changed) == 0 {
		return nil, nil // an empty commit touches nothing
	}
	dest, err := os.MkdirTemp("", "pack-operator-sync-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dest)
	if err := r.Checkout(ctx, sha, dest); err != nil {
		return nil, err
	}
	var releases []string
	err = filepath.WalkDir(dest, func(fp string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "release.yaml" {
			return err
		}
		rel, err := filepath.Rel(dest, filepath.Dir(fp))
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		dirs := []string{rel}
		if pack := packDir(dest, rel, fp); len(pack) > 0 {
			dirs = append(dirs, pack)
		}
		if changed == nil || touches(changed, dirs) {
			releases = append(releases, rel)
		}
		return nil
	})
	return releases, err
}

// packDir is the directory of the release's pack, relative to the repository's root — "" when the pack is not
// a directory of the repository (a registry reference) or names nothing.
func packDir(checkout, rel, releaseFile string) string {
	b, err := os.ReadFile(releaseFile)
	if err != nil {
		return ""
	}
	var doc struct {
		Pack string `json:"pack"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil || len(doc.Pack) == 0 || strings.Contains(doc.Pack, "://") {
		return ""
	}
	dir := path.Clean(path.Join(rel, doc.Pack))
	if dir == ".." || strings.HasPrefix(dir, "../") {
		return "" // the pack is not the repository's
	}
	if st, err := os.Stat(filepath.Join(checkout, filepath.FromSlash(dir))); err != nil || !st.IsDir() {
		return ""
	}
	return dir
}

// touches says whether any of the changed paths is under one of the directories.
func touches(changed, dirs []string) bool {
	for _, f := range changed {
		for _, d := range dirs {
			if f == d || strings.HasPrefix(f, d+"/") {
				return true
			}
		}
	}
	return false
}

// trailers is the К5 part of a commit: Pack-Approve on the main branch's commits, Pack-Merge on the head of a
// change-request branch. A trailer acts only from a commit signed with one of the project's approve_keys;
// unsigned or foreign-signed trailers are ignored, with the audit in the log.
func (s *Syncer) trailers(ctx context.Context, p *config.Project, r *git.Repo, c git.CommitInfo, main bool) error {
	var approvals [][2]string
	var merges []string
	for _, line := range strings.Split(c.Message, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, trailerApprove); ok {
			if f := strings.Fields(rest); len(f) == 2 {
				approvals = append(approvals, [2]string{f[0], f[1]})
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, trailerMerge); ok {
			if f := strings.Fields(rest); len(f) == 1 {
				merges = append(merges, f[0])
			}
		}
	}
	if len(approvals) == 0 && len(merges) == 0 {
		return nil
	}
	if main {
		merges = nil // Pack-Merge acts from a change-request branch, not from the main one
	}
	if len(approvals) == 0 && len(merges) == 0 {
		return nil
	}
	ok, err := git.VerifyCommit(ctx, r.Dir, c.SHA, p.ApproveKeys)
	if err != nil || !ok {
		s.log.Warn().Str("project", p.ID).Str("sha", c.SHA).Err(err).
			Msg("a trailer of a commit that does not verify against the project's keys — ignored")
		return nil
	}
	if s.policy == nil {
		return nil
	}
	for _, a := range approvals {
		if err := s.policy.Approve(ctx, p.ID, a[0], a[1], c.SHA, c.Author); err != nil {
			return err
		}
	}
	for _, m := range merges {
		if err := s.policy.Merge(ctx, p.ID, m, c.SHA, c.Author); err != nil {
			return err
		}
	}
	return nil
}

// knownRef is the sha of the branch the synchronizer has already processed, "" on first sight.
func (s *Syncer) knownRef(ctx context.Context, project, ref string) (string, error) {
	var sha string
	err := s.db.QueryRowContext(ctx, s.bind(`select sha from operator_sync where project = ? and ref = ?`),
		project, ref).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return sha, nil
}

// setRef records the branch's processed sha.
func (s *Syncer) setRef(ctx context.Context, project, ref, sha string) error {
	_, err := s.db.ExecContext(ctx, s.bind(`insert into operator_sync (project, ref, sha, updated_at) values (?, ?, ?, current_timestamp)
		on conflict (project, ref) do update set sha = excluded.sha, updated_at = excluded.updated_at`),
		project, ref, sha)
	return err
}

// bind numbers the ? of a query for PostgreSQL ($1, $2 …); SQLite takes them as they are.
func (s *Syncer) bind(query string) string {
	if s.db.SQLite() {
		return query
	}
	var out strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			fmt.Fprintf(&out, "$%d", n)
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// ---- change requests (B9)

// ResolveBranch is the sha of the project's remote branch, fresh fetched; "" when there is no such branch.
func (s *Syncer) ResolveBranch(ctx context.Context, projectID, branch string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cfg.Projects {
		if s.cfg.Projects[i].ID != projectID {
			continue
		}
		r, err := s.repo(ctx, &s.cfg.Projects[i])
		if err != nil {
			return "", err
		}
		if err := r.Fetch(ctx); err != nil {
			return "", err
		}
		if ok, err := r.HasRemoteBranch(ctx, branch); err != nil || !ok {
			return "", err
		}
		return r.Resolve(ctx, "refs/remotes/origin/"+branch)
	}
	return "", fmt.Errorf("no project %s", projectID)
}

// CreateBranch pushes sha as the project's remote branch; an existing branch is left alone.
func (s *Syncer) CreateBranch(ctx context.Context, projectID, branch, sha string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cfg.Projects {
		if s.cfg.Projects[i].ID != projectID {
			continue
		}
		r, err := s.repo(ctx, &s.cfg.Projects[i])
		if err != nil {
			return err
		}
		if err := r.Fetch(ctx); err != nil {
			return err
		}
		if ok, err := r.HasRemoteBranch(ctx, branch); err != nil || ok {
			return err
		}
		return r.Push(ctx, sha+":refs/heads/"+branch)
	}
	return fmt.Errorf("no project %s", projectID)
}

// CommitLock commits the release's lock file on the branch — fresh fetched first — and pushes; the answer is the
// branch's new head (B10). The next sync sees the commit as every other: a plan of the release at the new sha.
func (s *Syncer) CommitLock(ctx context.Context, projectID, branch, path string, content []byte, author, message string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cfg.Projects {
		if s.cfg.Projects[i].ID != projectID {
			continue
		}
		r, err := s.repo(ctx, &s.cfg.Projects[i])
		if err != nil {
			return "", err
		}
		if err := r.Fetch(ctx); err != nil {
			return "", err
		}
		sha, err := r.CommitFile(ctx, branch, path, content, author, message)
		if err != nil {
			return "", err
		}
		if err := r.Push(ctx, sha+":refs/heads/"+branch); err != nil {
			return "", err
		}
		return sha, nil
	}
	return "", fmt.Errorf("no project %s", projectID)
}

// MergeChange merges the branch's head into the project's main branch and pushes: the merge's sha (the head
// itself on a fast-forward, the main branch's own head when merged already). git.ErrMergeConflict marks the
// change as conflicted, nothing pushed.
func (s *Syncer) MergeChange(ctx context.Context, projectID, branch, author, message string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cfg.Projects {
		p := &s.cfg.Projects[i]
		if p.ID != projectID {
			continue
		}
		r, err := s.repo(ctx, p)
		if err != nil {
			return "", err
		}
		if err := r.Fetch(ctx); err != nil {
			return "", err
		}
		head, err := r.Resolve(ctx, "refs/remotes/origin/"+branch)
		if err != nil {
			return "", err
		}
		main := p.Branch
		if len(main) == 0 {
			main = "main"
		}
		sha, _, err := r.Merge(ctx, main, head, author, message)
		if err != nil {
			return "", err
		}
		mainHead, err := r.Resolve(ctx, "refs/remotes/origin/"+main)
		if err != nil {
			return "", err
		}
		if sha == mainHead {
			return sha, nil // merged already
		}
		if err := r.Push(ctx, sha+":refs/heads/"+main); err != nil {
			return "", err
		}
		return sha, nil
	}
	return "", fmt.Errorf("no project %s", projectID)
}
