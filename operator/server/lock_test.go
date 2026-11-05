package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	packer "github.com/arenadata/oci-packer"
	layout "github.com/arenadata/oci-packer/pkg/registry/oci-layout"
	"github.com/arenadata/oci-packer/pkg/registry/regtest"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/sdk"

	"github.com/horchestra/pack/operator/bus"
)

// A refresh end to end (B10): the release's pack is in a registry, the refresh job pins the tag's digest, the
// operator commits release.lock.json to the main branch (author and committer the operator), and the next sync
// plans the release at the lock commit's sha.
func TestRefreshLock(t *testing.T) {
	if _, err := packBin(); err != nil { // built before serveKV's t.Chdir
		t.Fatal(err)
	}
	host := serveKV(t)
	r := newAPIRigWith(t, "pack: oci://"+host+"/packs/kv:1.0.0\nregistry:\n  plain_http: true\n")

	resp := r.post(t, "/api/releases/dev:releases%2Fprod%2Fdb/requests", `{"action": "refresh"}`, http.StatusOK, http.StatusCreated)
	var req bus.Request
	if err := json.NewDecoder(resp.Body).Decode(&req); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := r.waitRequest(t, req.ID, bus.RequestDone, bus.RequestFailed); got.State != bus.RequestDone {
		t.Fatalf("the refresh: %+v", got)
	}

	// the lock is committed on the main branch, the operator the author
	lockSHA, lock, author := r.fileAtMain(t, "releases/prod/db/release.lock.json")
	if lockSHA == r.head {
		t.Fatal("the main branch never moved")
	}
	if !strings.Contains(lock, `"pack": "oci://`+host+`/packs/kv:1.0.0"`) || !strings.Contains(lock, `"digest": "sha256:`) {
		t.Fatalf("the lock: %s", lock)
	}
	if author != "pack-operator <pack-operator@localhost>" {
		t.Fatalf("the lock commit's author: %s", author)
	}

	// the sync plans the release at the lock commit's sha
	r.post(t, "/api/sync", `{"repo":"dev","ref":"main"}`, http.StatusAccepted)
	var rel struct {
		SHA     string `json:"sha"`
		Waiting []struct {
			ID    string `json:"id"`
			SHA   string `json:"sha"`
			State string `json:"state"`
		} `json:"waiting"`
	}
	deadline := time.Now().Add(90 * time.Second)
	var planID string
	for time.Now().Before(deadline) {
		if r.get(t, "/api/releases/dev:releases%2Fprod%2Fdb", &rel) && rel.SHA == lockSHA {
			for _, w := range rel.Waiting {
				if w.SHA == lockSHA {
					planID = w.ID
					break
				}
			}
			if len(planID) > 0 {
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(planID) == 0 {
		t.Fatalf("no plan of the lock commit's sha %s: %+v", lockSHA, rel)
	}
	r.waitRequest(t, planID, bus.RequestWaitingApproval)
}

// serveKV is a registry holding the fixture pack kv:1.0.0 — what internal/testpacks/regserver is for pack's own
// tests, cut to one pack that holds nothing: built into a layout by oci-packer, served over the distribution API.
func serveKV(t *testing.T) (host string) {
	t.Helper()
	gen := t.TempDir()
	if err := sdk.Generate(gen, &kv{}); err != nil {
		t.Fatal(err)
	}
	lay := filepath.Join(t.TempDir(), "layout")
	reg, err := layout.New("oci://" + lay + ":packs/kv:1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	res := reg.(*layout.Layout)
	raw, err := os.ReadFile(filepath.Join(gen, "kv", "pack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var p packer.Pack
	if err := yaml.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	p.Items = slices.DeleteFunc(p.Items, func(it packer.Descriptor) bool { return strings.HasPrefix(it.From, "oci://") })
	t.Chdir(filepath.Join(gen, "kv")) // the items are relative to the pack's directory
	desc, err := packer.Build(context.Background(), p, res)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.SetTag(context.Background(), desc); err != nil {
		t.Fatal(err)
	}
	return regtest.Host(regtest.Serve(t, res, "packs/kv"))
}

// fileAtMain is the main branch's head of the fixture's remote, the file's content there, and the head commit's
// author.
func (r *apiRig) fileAtMain(t *testing.T, name string) (sha, content, author string) {
	t.Helper()
	repo, err := git.PlainOpen(strings.TrimPrefix(r.cfg.Projects[0].Repo, "file://"))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatal(err)
	}
	c, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	tree, err := c.Tree()
	if err != nil {
		t.Fatal(err)
	}
	f, err := tree.FindEntry(name)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := repo.BlobObject(f.Hash)
	if err != nil {
		t.Fatal(err)
	}
	rd, err := blob.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	b, err := io.ReadAll(rd)
	if err != nil {
		t.Fatal(err)
	}
	return c.Hash.String(), string(b), c.Author.Name + " <" + c.Author.Email + ">"
}
