package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	ggconfig "github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/horchestra/pack/operator/bus"
)

// A change request end to end (B9): opened, a commit on its branch planned, merged into the main branch (the
// branches diverged: a two-parent merge commit), the merge's sha applied after the API's approval.
func TestChangeRequest(t *testing.T) {
	r := newAPIRig(t)

	// open: the branch is made at the main branch's head
	resp := r.post(t, "/api/changes", `{"project": "dev", "branch": "fix", "title": "the fix"}`, http.StatusCreated)
	var cr struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(cr.ID) == 0 {
		t.Fatal("no change id")
	}

	// a commit on the change's branch: the value moves
	cloneAndCommit(t, r, "fix", "releases/prod/db/values.yaml", "value: two\n", "the fix")
	// the main branch moves too, on another file: the merge is no fast-forward
	cloneAndCommit(t, r, "main", "NOTE.md", "a note\n", "a note of the main branch")

	// the sync plans the branch's head; the change's page shows the plan
	r.post(t, "/api/sync", `{"repo":"dev","ref":"main"}`, http.StatusAccepted)
	var detail struct {
		State string `json:"state"`
		SHA   string `json:"sha"`
		Plans []struct {
			Release string `json:"release"`
			SHA     string `json:"sha"`
			Digest  string `json:"digest"`
		} `json:"plans"`
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if r.get(t, "/api/changes/"+cr.ID, &detail) && len(detail.Plans) == 1 && len(detail.Plans[0].Digest) > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(detail.Plans) != 1 || detail.Plans[0].Release != "releases/prod/db" {
		t.Fatalf("the change's plans: %+v", detail.Plans)
	}
	changeSHA := detail.Plans[0].SHA

	// a comment, then the merge; the branches diverged, the merge is a commit of two parents
	resp = r.post(t, "/api/changes/"+cr.ID+"/comments", `{"body": "looks right"}`, http.StatusCreated)
	resp.Body.Close()
	resp = r.post(t, "/api/changes/"+cr.ID+"/merge", `{}`, http.StatusOK)
	resp.Body.Close()
	var merged struct {
		State string `json:"state"`
		SHA   string `json:"sha"`
	}
	if !r.get(t, "/api/changes/"+cr.ID, &merged) || merged.State != "merged" {
		t.Fatalf("the merged change: %+v", merged)
	}
	if len(merged.SHA) == 0 || merged.SHA == changeSHA {
		t.Fatalf("the merge's sha: %s (the branch's %s)", merged.SHA, changeSHA)
	}
	info, err := r.repoInfo(t, merged.SHA)
	if err != nil || len(info.Parents) != 2 {
		t.Fatalf("the merge commit: %v %+v", err, info)
	}

	// the main branch's sync requests the plan of the merge's sha; approved, it applies
	r.post(t, "/api/sync", `{"repo":"dev","ref":"main"}`, http.StatusAccepted)
	var rel struct {
		SHA     string `json:"sha"`
		Waiting []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"waiting"`
	}
	deadline = time.Now().Add(90 * time.Second)
	var requestID string
	for time.Now().Before(deadline) {
		if r.get(t, "/api/releases/dev:releases%2Fprod%2Fdb", &rel) && rel.SHA == merged.SHA {
			for _, w := range rel.Waiting {
				if w.State == bus.RequestWaitingApproval {
					requestID = w.ID
					break
				}
			}
			if len(requestID) > 0 {
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(requestID) == 0 {
		t.Fatalf("no request waits on the merge's sha: %+v", rel)
	}
	r.approve(t, requestID)

	deadline = time.Now().Add(90 * time.Second)
	var after struct {
		State        string `json:"state"`
		InstalledSHA string `json:"installed_sha"`
	}
	for time.Now().Before(deadline) {
		if r.get(t, "/api/releases/dev:releases%2Fprod%2Fdb", &after) && after.State == "applied" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if after.State != "applied" || after.InstalledSHA != merged.SHA {
		t.Fatalf("after the merge: %+v (the merge %s)", after, merged.SHA)
	}

	// the audit says the merge
	var audit []map[string]any
	if !r.get(t, "/api/audit", &audit) {
		t.Fatal("GET /api/audit")
	}
	found := false
	for _, e := range audit {
		if e["kind"] == "merge" && e["request"] == cr.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("the audit has no merge of %s", cr.ID)
	}
}

// cloneAndCommit clones the bare remote, commits content as name on the branch and pushes.
func cloneAndCommit(t *testing.T, r *apiRig, branch, name, content, message string) {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainCloneContext(t.Context(), dir, &git.CloneOptions{URL: r.cfg.Projects[0].Repo})
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	local := plumbing.NewBranchReferenceName(branch)
	if err := wt.Checkout(&git.CheckoutOptions{Branch: local}); err != nil {
		// no local branch: at the remote's, made
		if err := wt.Checkout(&git.CheckoutOptions{
			Branch: plumbing.NewRemoteReferenceName("origin", branch)}); err != nil {
			t.Fatal(err)
		}
		if err := wt.Checkout(&git.CheckoutOptions{Branch: local, Create: true}); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add(name); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{Name: "bob", Email: "bob@example.org"}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Push(&git.PushOptions{RefSpecs: []ggconfig.RefSpec{
		ggconfig.RefSpec(local.String() + ":refs/heads/" + branch)}}); err != nil {
		t.Fatal(err)
	}
}

// repoInfo inspects a commit of the fixture's remote.
func (r *apiRig) repoInfo(t *testing.T, sha string) (struct{ Parents []string }, error) {
	t.Helper()
	var out struct{ Parents []string }
	repo, err := git.PlainOpen(strings.TrimPrefix(r.cfg.Projects[0].Repo, "file://"))
	if err != nil {
		return out, err
	}
	c, err := repo.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return out, err
	}
	for _, p := range c.ParentHashes {
		out.Parents = append(out.Parents, p.String())
	}
	return out, nil
}
