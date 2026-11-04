package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/pkg/api"

	"github.com/horchestra/pack-operator/bus"
	"github.com/horchestra/pack-operator/config"
)

// releaseInfo is a release as the API lists it (К4).
type releaseInfo struct {
	Path         string   `json:"path"`
	Next         string   `json:"next,omitempty"`
	SHA          string   `json:"sha"`
	InstalledSHA string   `json:"installed_sha,omitempty"`
	State        string   `json:"state"`
	Health       string   `json:"health,omitempty"`
	LastJob      *jobBrief `json:"last_job,omitempty"`
}

// jobBrief is a job in one line.
type jobBrief struct {
	ID    string `json:"id"`
	State string `json:"state"`
	How   string `json:"how,omitempty"`
}

// releasesDoc reads a checkout's releases/*/  release.yaml files: path and installation id.
type releaseDoc struct {
	ID string `json:"id"`
}

// releases answers GET /api/projects/{p}/releases: the releases of the last synced sha, with the state store's
// word on each (К2, read only) and the last job.
func (s *Server) releases(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("p")
	proj := s.projectOf(pid)
	if proj == nil {
		fail(w, http.StatusNotFound, codeNotFound, "no project %s", pid)
		return
	}
	if _, ok := s.may(w, r, pid, "release:/*", "read"); !ok {
		return
	}
	ctx := r.Context()
	sha, err := s.head(ctx, pid)
	if err != nil {
		fail(w, http.StatusConflict, codeConflict, "%v", err)
		return
	}
	docs, err := s.releaseDocs(ctx, proj, sha)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	out := []releaseInfo{}
	for path, id := range docs {
		info := releaseInfo{Path: path, SHA: sha, State: "new"}
		if rec, err := s.state.recordOf(ctx, id); err != nil {
			fail(w, http.StatusInternalServerError, api.CodeError, "the state store: %v", err)
			return
		} else if rec != nil {
			info.InstalledSHA = rec.Revision
			info.State = "applied"
			if rec.Revision != sha {
				info.State = "stale"
			}
		}
		if jobs, err := s.jobsOf(ctx, pid, path, "", 1); err == nil && len(jobs) > 0 {
			info.LastJob = &jobBrief{ID: jobs[0].ID, State: jobs[0].State}
		}
		out = append(out, info)
	}
	reply(w, out)
}

// releaseDocs walks the checkout of sha for releases/*/*/release.yaml.
func (s *Server) releaseDocs(ctx context.Context, proj *config.Project, sha string) (map[string]string, error) {
	dir, err := os.MkdirTemp("", "pack-operator-releases-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	checkout := filepath.Join(dir, "checkout")
	if err := s.syncer.Checkout(ctx, proj.ID, sha, checkout); err != nil {
		return nil, err
	}
	out := map[string]string{}
	root := filepath.Join(checkout, "releases")
	err = filepath.WalkDir(root, func(fp string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "release.yaml" {
			return nil
		}
		rel, err := filepath.Rel(checkout, fp)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(fp)
		if err != nil {
			return err
		}
		var doc releaseDoc
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return nil // a release without a parseable release.yaml is pack's to report, not the list's
		}
		out[filepath.ToSlash(filepath.Dir(rel))] = doc.ID
		return nil
	})
	if os.IsNotExist(err) {
		return out, nil
	}
	return out, err
}

// release answers GET /api/releases/{rel}: the release with its open requests (К4).
func (s *Server) release(w http.ResponseWriter, r *http.Request) {
	_, proj, rel, ok := s.releaseAccess(w, r, "read")
	if !ok {
		return
	}
	ctx := r.Context()
	sha, err := s.head(ctx, proj.ID)
	if err != nil {
		fail(w, http.StatusConflict, codeConflict, "%v", err)
		return
	}
	docs, err := s.releaseDocs(ctx, proj, sha)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	id, known := docs[rel]
	if !known {
		fail(w, http.StatusNotFound, codeNotFound, "no release %s in %s at %s", rel, proj.ID, sha[:12])
		return
	}
	info := releaseInfo{Path: rel, SHA: sha, State: "new"}
	if rec, err := s.state.recordOf(ctx, id); err == nil && rec != nil {
		info.InstalledSHA = rec.Revision
		info.State = "applied"
		if rec.Revision != sha {
			info.State = "stale"
		}
	}
	requests, err := s.requestsOf(ctx, proj.ID, rel, 50)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	waiting := []bus.Request{}
	for _, req := range requests {
		if req.State == bus.RequestOpen || req.State == bus.RequestWaitingApproval {
			waiting = append(waiting, req)
		}
	}
	reply(w, struct {
		releaseInfo
		Waiting []bus.Request `json:"waiting"`
	}{info, waiting})
}

// sagas answers GET /api/releases/{rel}/sagas: the release's sagas from the journal (К2).
func (s *Server) sagas(w http.ResponseWriter, r *http.Request) {
	id, ok := s.releaseStateID(w, r)
	if !ok {
		return
	}
	runs, err := s.state.sagasOf(r.Context(), id)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "the state store: %v", err)
		return
	}
	if runs == nil {
		runs = []sagaRun{}
	}
	reply(w, runs)
}

// sagaEvents answers GET /api/releases/{rel}/sagas/{saga}/events: the saga's journal as an SSE stream of
// pkg/api events, replayed; Last-Event-ID skips what the client already holds (К4).
func (s *Server) sagaEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := s.releaseStateID(w, r)
	if !ok {
		return
	}
	entries, err := s.state.sagaEntries(r.Context(), id, r.PathValue("saga"), lastEventID(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "the state store: %v", err)
		return
	}
	fl, ok := sseHeaders(w)
	if !ok {
		fail(w, http.StatusInternalServerError, api.CodeError, "no streaming here")
		return
	}
	for i, e := range entries {
		ev := api.FromEntry(e)
		b, _ := json.Marshal(ev)
		sseSend(w, fl, lastEventID(r)+int64(i)+1, b)
	}
}

// facts answers GET /api/releases/{rel}/facts: the facts of the release's targets (pack_facts, К2).
func (s *Server) facts(w http.ResponseWriter, r *http.Request) {
	id, ok := s.releaseStateID(w, r)
	if !ok {
		return
	}
	facts, err := s.state.factsOf(r.Context(), id)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "the state store: %v", err)
		return
	}
	reply(w, struct {
		Facts map[string]json.RawMessage `json:"facts"`
	}{facts})
}

// releaseStateID guards the call and answers the release's installation id (the state store's key).
func (s *Server) releaseStateID(w http.ResponseWriter, r *http.Request) (string, bool) {
	_, proj, rel, ok := s.releaseAccess(w, r, "read")
	if !ok {
		return "", false
	}
	ctx := r.Context()
	sha, err := s.head(ctx, proj.ID)
	if err != nil {
		fail(w, http.StatusConflict, codeConflict, "%v", err)
		return "", false
	}
	docs, err := s.releaseDocs(ctx, proj, sha)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return "", false
	}
	id, known := docs[rel]
	if !known {
		fail(w, http.StatusNotFound, codeNotFound, "no release %s in %s", rel, proj.ID)
		return "", false
	}
	return id, true
}
