package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/horchestra/pack/pkg/api"

	"github.com/horchestra/pack/operator/bus"
)

// askRequest answers POST /api/releases/{rel}/requests: {action, sha, params} — a request (К3), idempotent by
// its key; 201 with the request (the one that stands, if the same work was asked before).
func (s *Server) askRequest(w http.ResponseWriter, r *http.Request) {
	user, proj, rel, ok := s.releaseAccess(w, r, "request")
	if !ok {
		return
	}
	var body struct {
		Action string     `json:"action"`
		SHA    string     `json:"sha"`
		Params bus.Params `json:"params"`
	}
	if err := decode(r, &body); err != nil || len(body.Action) == 0 {
		fail(w, http.StatusBadRequest, codeBadRequest, "the body is {\"action\": \"plan|apply|…\", \"sha\": \"…\", \"params\": {…}}")
		return
	}
	if len(body.SHA) == 0 {
		var err error
		if body.SHA, err = s.head(r.Context(), proj.ID); err != nil {
			fail(w, http.StatusConflict, codeConflict, "%v", err)
			return
		}
	}
	req, err := bus.NewRequest(proj.ID, body.SHA, bus.Target{Release: rel}, body.Action, body.Params, user)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	stood, fresh, err := s.bus.Submit(r.Context(), req)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	if fresh {
		w.WriteHeader(http.StatusCreated)
	}
	reply(w, stood)
}

// request answers GET /api/requests/{id}: the request, its jobs, what it waits on (К4).
func (s *Server) request(w http.ResponseWriter, r *http.Request) {
	req, err := s.bus.GetRequest(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, codeNotFound, "no request %s", r.PathValue("id"))
		return
	}
	if _, ok := s.may(w, r, req.Repo, "release:"+req.Target.Release, "read"); !ok {
		return
	}
	jobs, err := s.jobsOfRequest(r.Context(), req.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	var digest string // what the request waits on: the plan's digest, while it waits (К4)
	_ = s.db.QueryRowContext(r.Context(), s.bind(`select digest from operator_requests where id = ?`), req.ID).Scan(&digest)
	reply(w, struct {
		bus.Request
		Digest string    `json:"digest"`
		Jobs   []bus.Job `json:"jobs"`
	}{req, digest, jobs})
}

// approve answers POST /api/requests/{id}/approve: {digest, sha} — an approval through the API (К3); the
// policy checks the right (К7) and the digest against the plan that waits.
func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	user, viaToken, ok := s.userAuth(w, r)
	if !ok {
		return
	}
	user = s.impersonateAny(r, user, viaToken)
	var body struct {
		Digest string `json:"digest"`
		SHA    string `json:"sha"`
	}
	if err := decode(r, &body); err != nil || len(body.Digest) == 0 || len(body.SHA) == 0 {
		fail(w, http.StatusBadRequest, codeBadRequest, "the body is {\"digest\": \"sha256:…\", \"sha\": \"…\"}")
		return
	}
	if err := s.pol.ApproveAPI(r.Context(), user, r.PathValue("id"), body.Digest, body.SHA); err != nil {
		fail(w, http.StatusForbidden, codeForbidden, "%v", err)
		return
	}
	reply(w, struct {
		Approved string `json:"approved"`
	}{r.PathValue("id")})
}

// jobs answers GET /api/jobs?release=&state= (К4): the policy's projections, the newest first; release is
// <project>:<path> as everywhere.
func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var repo, rel string
	if v := q.Get("release"); len(v) > 0 {
		var ok bool
		repo, rel, ok = cutRelease(v)
		if !ok {
			fail(w, http.StatusBadRequest, codeBadRequest, "release is <project>:<path>")
			return
		}
		if _, ok := s.may(w, r, repo, "release:"+rel, "read"); !ok {
			return
		}
		jobs, err := s.jobsOf(r.Context(), repo, rel, q.Get("state"), 100)
		if err != nil {
			fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
			return
		}
		reply(w, jobs)
		return
	}
	// without a release: the jobs of the projects the user may read
	user, viaToken, authed := s.userAuth(w, r)
	if !authed {
		return
	}
	out := []bus.Job{}
	for _, p := range s.cfg.Projects {
		ok, err := s.az.Enforce(s.impersonate(r, user, viaToken, p.ID), p.ID, "release:/*", "read")
		if err != nil {
			fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
			return
		}
		if !ok {
			continue
		}
		jobs, err := s.jobsOf(r.Context(), p.ID, "", q.Get("state"), 100)
		if err != nil {
			fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
			return
		}
		out = append(out, jobs...)
	}
	reply(w, out)
}

// cutRelease splits <project>:<path>.
func cutRelease(v string) (string, string, bool) {
	repo, rel, ok := strings.Cut(v, ":")
	return repo, rel, ok && len(repo) > 0 && len(rel) > 0
}

// job answers GET /api/jobs/{id} (К4).
func (s *Server) job(w http.ResponseWriter, r *http.Request) {
	job, err := s.bus.GetJob(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, codeNotFound, "no job %s", r.PathValue("id"))
		return
	}
	repo, rel, err := s.releaseOfJob(r.Context(), job.ID)
	if err == nil && len(repo) > 0 {
		if _, ok := s.may(w, r, repo, "release:"+rel, "read"); !ok {
			return
		}
	}
	reply(w, job)
}

// jobEvents answers GET /api/jobs/{id}/events: the job's events as SSE — id: the event's seq, data: the
// pkg/api event; a reconnect's Last-Event-ID is honored (К4). The stream ends with the job's terminal state.
func (s *Server) jobEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	job, err := s.bus.GetJob(ctx, r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, codeNotFound, "no job %s", r.PathValue("id"))
		return
	}
	repo, rel, err := s.releaseOfJob(ctx, job.ID)
	if err == nil && len(repo) > 0 {
		if _, ok := s.may(w, r, repo, "release:"+rel, "read"); !ok {
			return
		}
	}
	fl, ok := sseHeaders(w)
	if !ok {
		fail(w, http.StatusInternalServerError, api.CodeError, "no streaming here")
		return
	}
	seen := lastEventID(r)
	terminal := func(state string) bool {
		switch state {
		case bus.JobSucceeded, bus.JobFailed, bus.JobCanceled, bus.JobLost:
			return true
		}
		return false
	}
	for {
		events, err := s.jobEventsOf(ctx, job.ID, seen)
		if err != nil {
			return
		}
		for _, ev := range events {
			sseSend(w, fl, ev.Seq, ev.Event)
			seen = ev.Seq
		}
		cur, err := s.bus.GetJob(ctx, job.ID)
		if err == nil && terminal(cur.State) && len(events) == 0 {
			return // the job ended and everything it said is sent
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// cancelJob answers POST /api/jobs/{id}/cancel: the record turns canceled; the worker's heartbeat watch
// interrupts the run the runtime's way (К6).
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	job, err := s.bus.GetJob(ctx, r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, codeNotFound, "no job %s", r.PathValue("id"))
		return
	}
	repo, rel, err := s.releaseOfJob(ctx, job.ID)
	if err != nil || len(repo) == 0 {
		fail(w, http.StatusNotFound, codeNotFound, "no job %s", r.PathValue("id"))
		return
	}
	if _, ok := s.may(w, r, repo, "release:"+rel, "request"); !ok {
		return
	}
	switch job.State {
	case bus.JobSucceeded, bus.JobFailed, bus.JobCanceled, bus.JobLost:
		fail(w, http.StatusConflict, codeConflict, "job %s is %s", job.ID, job.State)
		return
	}
	job.State = bus.JobCanceled
	if err := s.bus.PublishJob(ctx, job); err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	reply(w, job)
}
