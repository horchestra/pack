package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/horchestra/pack/pkg/api"

	"github.com/horchestra/pack-operator/bus"
	"github.com/horchestra/pack-operator/config"
)

// The operator's own error codes of К4 (a Failure's code).
const (
	codeUnauthorized = "unauthorized"
	codeForbidden    = "forbidden"
	codeBusy         = "busy"
	codeStale        = "stale"
	codeConflict     = "conflict"
	codeNotFound     = "not_found"
	codeBadRequest   = "bad_request"
)

// routes is the whole API of К4 (the change requests' endpoints come with B9).
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/version", s.version)
	mux.HandleFunc("POST /api/sync", s.askSync)
	mux.HandleFunc("GET /api/projects", s.projects)
	mux.HandleFunc("PUT /api/projects", s.putProject)
	mux.HandleFunc("GET /api/projects/{p}/releases", s.releases)
	mux.HandleFunc("GET /api/releases/{rel}", s.release)
	mux.HandleFunc("GET /api/releases/{rel}/next", s.packOp("next"))
	mux.HandleFunc("GET /api/releases/{rel}/status", s.packOp("status"))
	mux.HandleFunc("GET /api/releases/{rel}/history", s.packOp("history"))
	mux.HandleFunc("GET /api/releases/{rel}/logs", s.packOp("logs"))
	mux.HandleFunc("GET /api/releases/{rel}/actions", s.packOp("run"))
	mux.HandleFunc("GET /api/releases/{rel}/check", s.packOp("check"))
	mux.HandleFunc("GET /api/releases/{rel}/template", s.packOp("template"))
	mux.HandleFunc("GET /api/releases/{rel}/graph", s.packOp("graph"))
	mux.HandleFunc("GET /api/releases/{rel}/schema", s.packOp("schema"))
	mux.HandleFunc("GET /api/releases/{rel}/explain", s.packOp("explain"))
	mux.HandleFunc("GET /api/releases/{rel}/sagas", s.sagas)
	mux.HandleFunc("GET /api/releases/{rel}/sagas/{saga}/events", s.sagaEvents)
	mux.HandleFunc("GET /api/releases/{rel}/facts", s.facts)
	mux.HandleFunc("POST /api/releases/{rel}/requests", s.askRequest)
	mux.HandleFunc("GET /api/requests/{id}", s.request)
	mux.HandleFunc("POST /api/requests/{id}/approve", s.approve)
	mux.HandleFunc("GET /api/jobs", s.jobs)
	mux.HandleFunc("GET /api/jobs/{id}", s.job)
	mux.HandleFunc("GET /api/jobs/{id}/events", s.jobEvents)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.cancelJob)
	mux.HandleFunc("GET /api/secrets", s.secrets)
	mux.HandleFunc("POST /api/secrets", s.putSecret)
	mux.HandleFunc("GET /api/audit", s.audit)
}

// fail answers with a pkg/api.Failure of code and the HTTP status.
func fail(w http.ResponseWriter, status int, code, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(api.Failure{Error: api.Error{Code: code, Message: fmt.Sprintf(format, args...)}})
	_, _ = w.Write(append(b, '\n'))
}

// user is who the call is as (К7: until stage 3 — the dev user of auth: none, or the X-Pack-User pack-ui
// passes over a trusted connection).
func (s *Server) user(r *http.Request) string {
	if s.cfg.Auth == "none" {
		if v := r.Header.Get("X-Pack-User"); len(v) > 0 {
			return v
		}
		return s.cfg.DevUser
	}
	return "" // oidc is stage 3
}

// may enforces obj/act in the domain on the call's user; a denial is answered (401/403) and false returned.
func (s *Server) may(w http.ResponseWriter, r *http.Request, dom, obj, act string) (string, bool) {
	user := s.user(r)
	if len(user) == 0 {
		fail(w, http.StatusUnauthorized, codeUnauthorized, "who are you? (auth: %s)", s.cfg.Auth)
		return "", false
	}
	ok, err := s.az.Enforce(user, dom, obj, act)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "the policy: %v", err)
		return "", false
	}
	if !ok {
		fail(w, http.StatusForbidden, codeForbidden, "%s may not %s %s in %s", user, act, obj, dom)
		return "", false
	}
	return user, true
}

// relID parses the {rel} of the routes: "<project>:<release path>", URL-encoded (К4).
func relID(r *http.Request) (project, release string, ok bool) {
	project, release, ok = strings.Cut(r.PathValue("rel"), ":")
	ok = ok && len(project) > 0 && len(release) > 0
	return
}

// projectOf is the config's project of id.
func (s *Server) projectOf(id string) *config.Project {
	for i := range s.cfg.Projects {
		if s.cfg.Projects[i].ID == id {
			return &s.cfg.Projects[i]
		}
	}
	return nil
}

// releaseAccess guards a release endpoint: the project exists, the user may read (or wider) it.
func (s *Server) releaseAccess(w http.ResponseWriter, r *http.Request, act string) (string, *config.Project, string, bool) {
	pid, rel, ok := relID(r)
	if !ok {
		fail(w, http.StatusBadRequest, codeBadRequest, "a release is <project>:<path>, URL-encoded")
		return "", nil, "", false
	}
	proj := s.projectOf(pid)
	if proj == nil {
		fail(w, http.StatusNotFound, codeNotFound, "no project %s", pid)
		return "", nil, "", false
	}
	user, ok := s.may(w, r, pid, "release:"+rel, act)
	if !ok {
		return "", nil, "", false
	}
	return user, proj, rel, true
}

// decode reads the request's JSON body.
func decode(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// jobEventsOf are the job's job_event records from afterSeq on (the SSE replay).
func (s *Server) jobEventsOf(ctx context.Context, jobID string, afterSeq int64) ([]bus.JobEvent, error) {
	match := `json_extract(body, '$.kind') = 'job_event' and json_extract(body, '$.job') = ? and json_extract(body, '$.seq') > ?`
	if !s.db.SQLite() {
		match = `body::jsonb ->> 'kind' = 'job_event' and body::jsonb ->> 'job' = ? and (body::jsonb ->> 'seq')::bigint > ?`
	}
	rows, err := s.db.QueryContext(ctx, `select body from bus_events where `+match+` order by seq`, jobID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bus.JobEvent
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var ev bus.JobEvent
		if err := json.Unmarshal(body, &ev); err == nil {
			out = append(out, ev)
		}
	}
	return out, rows.Err()
}

// requestsOf are the policy's projections of a release's requests, the newest first.
func (s *Server) requestsOf(ctx context.Context, repo, release string, limit int) ([]bus.Request, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`select body from operator_requests
		where repo = ? and release = ? order by updated_at desc limit ?`), repo, release, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bus.Request
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var req bus.Request
		if err := json.Unmarshal(body, &req); err == nil {
			out = append(out, req)
		}
	}
	return out, rows.Err()
}

// jobsOf are the policy's projections of jobs, by the release and/or the state, the newest first.
func (s *Server) jobsOf(ctx context.Context, repo, release, state string, limit int) ([]bus.Job, error) {
	where := []string{"1 = 1"}
	args := []any{}
	if len(repo) > 0 {
		where = append(where, "repo = ?")
		args = append(args, repo)
	}
	if len(release) > 0 {
		where = append(where, "release = ?")
		args = append(args, release)
	}
	if len(state) > 0 {
		where = append(where, "state = ?")
		args = append(args, state)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.bind(`select body from operator_jobs where `+strings.Join(where, " and ")+
		` order by updated_at desc limit ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bus.Job
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var job bus.Job
		if err := json.Unmarshal(body, &job); err == nil {
			out = append(out, job)
		}
	}
	return out, rows.Err()
}

// jobsOfRequest are the jobs one request queued.
func (s *Server) jobsOfRequest(ctx context.Context, requestID string) ([]bus.Job, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`select body from operator_jobs where request = ? order by updated_at`), requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bus.Job
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var job bus.Job
		if err := json.Unmarshal(body, &job); err == nil {
			out = append(out, job)
		}
	}
	return out, rows.Err()
}

// releaseOfJob is the job's release (the projection's).
func (s *Server) releaseOfJob(ctx context.Context, jobID string) (repo, release string, err error) {
	err = s.db.QueryRowContext(ctx, s.bind(`select repo, release from operator_jobs where id = ?`), jobID).Scan(&repo, &release)
	return
}

// bind is the placeholder style of the database driver.
func (s *Server) bind(query string) string {
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

// sseHeaders opens an SSE stream.
func sseHeaders(w http.ResponseWriter) (http.Flusher, bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fl, ok := w.(http.Flusher)
	return fl, ok
}

// sseSend is one event: id: <id>, data: <json>.
func sseSend(w http.ResponseWriter, fl http.Flusher, id int64, data json.RawMessage) {
	fmt.Fprintf(w, "id: %d\ndata: %s\n\n", id, data)
	fl.Flush()
}

// lastEventID is the Last-Event-ID of a reconnecting SSE (К4).
func lastEventID(r *http.Request) int64 {
	var id int64
	_, _ = fmt.Sscan(r.Header.Get("Last-Event-ID"), &id)
	return id
}
