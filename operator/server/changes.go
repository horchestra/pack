package server

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/horchestra/pack/pkg/api"
)

// change is a change request (B9): a branch, its author and title, open | merged | closed | conflict.
type change struct {
	ID      string    `json:"id"`
	Project string    `json:"project"`
	Branch  string    `json:"branch"`
	Author  string    `json:"author"`
	Title   string    `json:"title"`
	State   string    `json:"state"`
	SHA     string    `json:"sha"`
	At      time.Time `json:"at"`
}

// changeOf is the change request of an id; none — sql.ErrNoRows.
func (s *Server) changeOf(r *http.Request, id string) (*change, error) {
	var c change
	var at string
	err := s.db.QueryRowContext(r.Context(), s.bind(`select id, project, branch, author, title, state, sha, created_at
		from operator_changes where id = ?`), id).Scan(&c.ID, &c.Project, &c.Branch, &c.Author, &c.Title, &c.State, &c.SHA, &at)
	if err != nil {
		return nil, err
	}
	c.At = journalTime(at)
	return &c, nil
}

// changes answers GET /api/changes?project= — the project's change requests, the newest first.
func (s *Server) changes(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if len(project) == 0 {
		fail(w, http.StatusBadRequest, codeBadRequest, "the project: ?project=")
		return
	}
	if _, ok := s.may(w, r, project, "change:/*", "read"); !ok {
		return
	}
	rows, err := s.db.QueryContext(r.Context(), s.bind(`select id, project, branch, author, title, state, sha, created_at
		from operator_changes where project = ? order by created_at desc`), project)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	defer rows.Close()
	out := []change{}
	for rows.Next() {
		var c change
		var at string
		if err := rows.Scan(&c.ID, &c.Project, &c.Branch, &c.Author, &c.Title, &c.State, &c.SHA, &at); err != nil {
			fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
			return
		}
		c.At = journalTime(at)
		out = append(out, c)
	}
	reply(w, out)
}

// openChange answers POST /api/changes: {project, branch, title} — the change request of the branch; a branch
// the remote has not is made at the main branch's head. Opening a change that is open already answers it.
func (s *Server) openChange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string `json:"project"`
		Branch  string `json:"branch"`
		Title   string `json:"title"`
	}
	if err := decode(r, &body); err != nil || len(body.Project) == 0 || len(body.Branch) == 0 || len(body.Title) == 0 {
		fail(w, http.StatusBadRequest, codeBadRequest, "the body is {\"project\": …, \"branch\": …, \"title\": …}")
		return
	}
	if strings.Contains(body.Branch, "..") || strings.HasPrefix(body.Branch, "/") || strings.HasPrefix(body.Branch, "-") {
		fail(w, http.StatusBadRequest, codeBadRequest, "the branch: %q", body.Branch)
		return
	}
	user, ok := s.may(w, r, body.Project, "change:/*", "read")
	if !ok {
		return
	}
	proj := s.projectOf(body.Project)
	if proj == nil {
		fail(w, http.StatusNotFound, codeNotFound, "no project %s", body.Project)
		return
	}
	var existing string
	err := s.db.QueryRowContext(r.Context(), s.bind(`select id from operator_changes
		where project = ? and branch = ? and state = 'open'`), body.Project, body.Branch).Scan(&existing)
	if err == nil {
		http.Redirect(w, r, "/api/changes/"+existing, http.StatusSeeOther)
		return
	}
	head, err := s.syncer.ResolveBranch(r.Context(), body.Project, body.Branch)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "the branch: %v", err)
		return
	}
	if len(head) == 0 {
		main := proj.Branch
		if len(main) == 0 {
			main = "main"
		}
		mainHead, err := s.syncer.ResolveBranch(r.Context(), body.Project, main)
		if err != nil || len(mainHead) == 0 {
			fail(w, http.StatusInternalServerError, api.CodeError, "the main branch: %v", err)
			return
		}
		if err := s.syncer.CreateBranch(r.Context(), body.Project, body.Branch, mainHead); err != nil {
			fail(w, http.StatusInternalServerError, api.CodeError, "the branch: %v", err)
			return
		}
		head = mainHead
	}
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	id := "cr-" + hex.EncodeToString(b[:])
	if _, err := s.db.ExecContext(r.Context(), s.bind(`insert into operator_changes (id, project, branch, author, title, sha)
		values (?, ?, ?, ?, ?, ?)`), id, body.Project, body.Branch, user, body.Title, head); err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	reply(w, change{ID: id, Project: body.Project, Branch: body.Branch, Author: user, Title: body.Title,
		State: "open", SHA: head, At: time.Now().UTC()})
}

// changeDetail answers GET /api/changes/{cr}: the change, the plans of its branch's head, the comments.
func (s *Server) changeDetail(w http.ResponseWriter, r *http.Request) {
	c, err := s.changeOf(r, r.PathValue("cr"))
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, http.StatusNotFound, codeNotFound, "no change %s", r.PathValue("cr"))
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	if _, ok := s.may(w, r, c.Project, "change:"+c.ID, "read"); !ok {
		return
	}
	type plan struct {
		Release string          `json:"release"`
		SHA     string          `json:"sha"`
		Digest  string          `json:"digest"`
		Plan    json.RawMessage `json:"plan"`
	}
	plans := []plan{}
	rows, err := s.db.QueryContext(r.Context(), s.bind(`select release, sha, digest, plan from operator_change_plans
		where change = ? order by release`), c.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var p plan
		var raw string
		if err := rows.Scan(&p.Release, &p.SHA, &p.Digest, &raw); err != nil {
			fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
			return
		}
		p.Plan = json.RawMessage(raw)
		plans = append(plans, p)
	}
	type comment struct {
		Author string    `json:"author"`
		Body   string    `json:"body"`
		At     time.Time `json:"at"`
	}
	comments := []comment{}
	crows, err := s.db.QueryContext(r.Context(), s.bind(`select author, body, at from operator_change_comments
		where change = ? order by at`), c.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	defer crows.Close()
	for crows.Next() {
		var cm comment
		var at string
		if err := crows.Scan(&cm.Author, &cm.Body, &at); err != nil {
			fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
			return
		}
		cm.At = journalTime(at)
		comments = append(comments, cm)
	}
	reply(w, struct {
		*change
		Plans    []plan    `json:"plans"`
		Comments []comment `json:"comments"`
	}{c, plans, comments})
}

// commentChange answers POST /api/changes/{cr}/comments: {body}.
func (s *Server) commentChange(w http.ResponseWriter, r *http.Request) {
	c, err := s.changeOf(r, r.PathValue("cr"))
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, http.StatusNotFound, codeNotFound, "no change %s", r.PathValue("cr"))
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	user, ok := s.may(w, r, c.Project, "change:"+c.ID, "read")
	if !ok {
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if err := decode(r, &body); err != nil || len(body.Body) == 0 {
		fail(w, http.StatusBadRequest, codeBadRequest, "the body is {\"body\": …}")
		return
	}
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	if _, err := s.db.ExecContext(r.Context(), s.bind(`insert into operator_change_comments (id, change, author, body)
		values (?, ?, ?, ?)`), "cc-"+hex.EncodeToString(b[:]), c.ID, user, body.Body); err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	reply(w, struct {
		Comment string `json:"comment"`
	}{"added"})
}

// closeChange answers POST /api/changes/{cr}/close: the change's author or a merger closes it.
func (s *Server) closeChange(w http.ResponseWriter, r *http.Request) {
	s.setChangeState(w, r, "closed")
}

// mergeChange answers POST /api/changes/{cr}/merge: by the merge right; the policy merges the branch into the
// main branch and pushes. A conflict is 409, the change marked.
func (s *Server) mergeChange(w http.ResponseWriter, r *http.Request) {
	c, err := s.changeOf(r, r.PathValue("cr"))
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, http.StatusNotFound, codeNotFound, "no change %s", r.PathValue("cr"))
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	user, ok := s.may(w, r, c.Project, "change:"+c.ID, "merge")
	if !ok {
		return
	}
	if err := s.pol.Merge(r.Context(), c.Project, c.ID, "", user); err != nil {
		if strings.Contains(err.Error(), "changed the same files") {
			fail(w, http.StatusConflict, codeConflict, "%v", err)
			return
		}
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	reply(w, struct {
		Merged string `json:"merged"`
	}{c.ID})
}

// setChangeState moves a change to a state (close): the author may, a merger may.
func (s *Server) setChangeState(w http.ResponseWriter, r *http.Request, state string) {
	c, err := s.changeOf(r, r.PathValue("cr"))
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, http.StatusNotFound, codeNotFound, "no change %s", r.PathValue("cr"))
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	user, viaToken, authed := s.userAuth(w, r)
	if !authed {
		return
	}
	user = s.impersonate(r, user, viaToken, c.Project)
	if user != c.Author {
		if _, ok := s.may(w, r, c.Project, "change:"+c.ID, "merge"); !ok {
			return
		}
	}
	if _, err := s.db.ExecContext(r.Context(), s.bind(`update operator_changes set state = ?, updated_at = current_timestamp
		where id = ? and state = 'open'`), state, c.ID); err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	reply(w, struct {
		State string `json:"state"`
	}{state})
}
