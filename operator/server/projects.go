package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/horchestra/pack/pkg/api"

	"github.com/horchestra/pack-operator/config"
	"github.com/horchestra/pack-operator/git"
)

// projects answers GET /api/projects: the projects the user may read (К4).
func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	user, viaToken, authed := s.userAuth(w, r)
	if !authed {
		return
	}
	out := []config.Project{}
	for _, p := range s.cfg.Projects {
		ok, err := s.az.Enforce(s.impersonate(r, user, viaToken, p.ID), p.ID, "project", "read")
		if err != nil {
			fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
			return
		}
		if ok {
			out = append(out, p)
		}
	}
	reply(w, out)
}

// mayAny enforces obj/act in any of the configured domains (the operator-wide objects — a project, a
// secret — belong to no single one; К7).
func (s *Server) mayAny(w http.ResponseWriter, r *http.Request, obj, act string) (string, bool) {
	user, viaToken, authed := s.userAuth(w, r)
	if !authed {
		return "", false
	}
	for _, p := range s.cfg.Projects {
		u := s.impersonate(r, user, viaToken, p.ID)
		ok, err := s.az.Enforce(u, p.ID, obj, act)
		if err != nil {
			fail(w, http.StatusInternalServerError, api.CodeError, "the policy: %v", err)
			return "", false
		}
		if ok {
			return u, true
		}
	}
	fail(w, http.StatusForbidden, codeForbidden, "%s may not %s %s in any domain", user, act, obj)
	return "", false
}

// putProject answers PUT /api/projects (admin): a project is written to the database (the file's projects are
// the source at start; the database's win on a same id) and lives at once.
func (s *Server) putProject(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mayAny(w, r, "project", "admin"); !ok {
		return
	}
	var p config.Project
	if err := decode(r, &p); err != nil || len(p.ID) == 0 || len(p.Repo) == 0 {
		fail(w, http.StatusBadRequest, codeBadRequest, "a project is {\"id\", \"repo\", …}")
		return
	}
	body, err := json.Marshal(p)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	if _, err := s.db.ExecContext(r.Context(), s.bind(`insert into operator_projects (id, body) values (?, ?)
		on conflict (id) do update set body = excluded.body, updated_at = current_timestamp`), p.ID, body); err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	if err := s.az.SeedProject(p.ID); err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "the policy of %s: %v", p.ID, err)
		return
	}
	found := false
	for i := range s.cfg.Projects {
		if s.cfg.Projects[i].ID == p.ID {
			s.cfg.Projects[i] = p
			found = true
		}
	}
	if !found {
		s.cfg.Projects = append(s.cfg.Projects, p)
	}
	w.WriteHeader(http.StatusCreated)
	reply(w, p)
}

// loadProjects merges the database's projects into the configuration's at start (К8: the file is the source,
// the database's win on a same id).
func (s *Server) loadProjects(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `select body from operator_projects`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return err
		}
		var p config.Project
		if err := json.Unmarshal(body, &p); err != nil {
			continue
		}
		found := false
		for i := range s.cfg.Projects {
			if s.cfg.Projects[i].ID == p.ID {
				s.cfg.Projects[i] = p
				found = true
			}
		}
		if !found {
			s.cfg.Projects = append(s.cfg.Projects, p)
		}
	}
	return rows.Err()
}

// secrets answers GET /api/secrets?prefix= (К4): names and keys, never values. The store is the operator's
// database (К5); OpenBao lists nothing here.
func (s *Server) secrets(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mayAny(w, r, "secret:/*", "read"); !ok {
		return
	}
	store, ok := s.creds.(*git.DBSecrets)
	if !ok {
		reply(w, []git.SecretInfo{})
		return
	}
	infos, err := store.List(r.Context(), r.URL.Query().Get("prefix"))
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	if infos == nil {
		infos = []git.SecretInfo{}
	}
	reply(w, infos)
}

// putSecret answers POST /api/secrets: {name, fields} — a secret is created (the fields are Credentials':
// username, token, ssh_key, passphrase, known_hosts).
func (s *Server) putSecret(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.mayAny(w, r, "secret:/*", "admin"); !ok {
		return
	}
	store, ok := s.creds.(*git.DBSecrets)
	if !ok {
		fail(w, http.StatusConflict, codeConflict, "the secrets live in OpenBao: write them there")
		return
	}
	var body struct {
		Name   string `json:"name"`
		Fields struct {
			Username   string `json:"username"`
			Token      string `json:"token"`
			SSHKey     string `json:"ssh_key"`
			Passphrase string `json:"passphrase"`
			KnownHosts string `json:"known_hosts"`
		} `json:"fields"`
	}
	if err := decode(r, &body); err != nil || len(body.Name) == 0 {
		fail(w, http.StatusBadRequest, codeBadRequest, "a secret is {\"name\", \"fields\": {\"token\": …}}")
		return
	}
	c := &git.Credentials{
		Username:   body.Fields.Username,
		Token:      body.Fields.Token,
		SSHKey:     body.Fields.SSHKey,
		Passphrase: body.Fields.Passphrase,
		KnownHosts: body.Fields.KnownHosts,
	}
	if err := store.Put(r.Context(), body.Name, c); err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	reply(w, git.SecretInfo{Name: body.Name})
}

// audit answers GET /api/audit?since= (К4): the requests and approvals the Bus saw since (RFC 3339), the
// newest first.
func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	since := time.Time{}
	if v := r.URL.Query().Get("since"); len(v) > 0 {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			fail(w, http.StatusBadRequest, codeBadRequest, "since is RFC 3339: %v", err)
			return
		}
		since = t
	}
	rows, err := s.db.QueryContext(r.Context(), s.bind(`select kind, body, at from bus_events
		where kind in ('request', 'approval', 'merge') and at > ? order by seq desc limit 500`), since)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var kind string
		var body json.RawMessage
		var at time.Time
		if err := rows.Scan(&kind, &body, &at); err != nil {
			fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
			return
		}
		out = append(out, body)
	}
	reply(w, out)
}
