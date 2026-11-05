package server

import (
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/horchestra/pack/pkg/api"
)

// The authentication of К7's stage 3 (C9): the local users prove themselves by a bcrypt password
// (POST /api/login), the OIDC ones by the provider's code flow (/api/auth/oidc/{login,callback}); a login
// opens a session — an httpOnly cookie, its mutations need the session's X-CSRF-Token. CI carries a
// bearer token of /api/tokens; no cookie, no CSRF. A token of an admin of the domain may pass X-Pack-User
// and act as them — that is pack-ui serving its own users (К7: на этапе 3 — токеном).

// login answers POST /api/login {user, password}: the session's cookie and CSRF token, or 401.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Auth != "local" {
		fail(w, http.StatusNotFound, codeNotFound, "no local users with auth: %s", s.cfg.Auth)
		return
	}
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, http.StatusBadRequest, codeBadRequest, "the body is {\"user\", \"password\"}")
		return
	}
	for _, u := range s.cfg.Local.Users {
		if u.Name == body.User && bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(body.Password)) == nil {
			s.openSession(w, r, body.User)
			return
		}
	}
	fail(w, http.StatusUnauthorized, codeUnauthorized, "the user or the password is wrong")
}

// openSession is the session of a login, whichever way it proved itself.
func (s *Server) openSession(w http.ResponseWriter, r *http.Request, user string) {
	id, csrf, err := s.an.NewSession(r.Context(), user)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Expires: time.Now().Add(24 * time.Hour), Secure: r.TLS != nil,
	})
	reply(w, map[string]string{"user": user, "csrf": csrf})
}

// logout answers POST /api/logout: the session dropped, the cookie cleared.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.an.DropSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	reply(w, map[string]string{"bye": "bye"})
}

// oidcLogin answers GET /api/auth/oidc/login: off to the provider.
func (s *Server) oidcLogin(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		fail(w, http.StatusNotFound, codeNotFound, "no OIDC with auth: %s", s.cfg.Auth)
		return
	}
	s.oidc.Login(w, r)
}

// oidcCallback answers GET /api/auth/oidc/callback: the id token says who; its groups, mapped by the
// configuration, are granted in every project's domain; the session opens.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		fail(w, http.StatusNotFound, codeNotFound, "no OIDC with auth: %s", s.cfg.Auth)
		return
	}
	id, err := s.oidc.Callback(w, r)
	if err != nil {
		fail(w, http.StatusUnauthorized, codeUnauthorized, "%v", err)
		return
	}
	for _, g := range id.Groups {
		role, ok := s.cfg.OIDC.Groups[g]
		if !ok {
			continue
		}
		for _, p := range s.cfg.Projects {
			if err := s.az.Grant(id.User, role, p.ID); err != nil {
				fail(w, http.StatusInternalServerError, api.CodeError, "the grant of %s: %v", role, err)
				return
			}
		}
	}
	s.openSession(w, r, id.User)
}

// tokens answers GET /api/tokens — the caller's own, the token itself never among them.
func (s *Server) tokens(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.userAuth(w, r)
	if !ok {
		return
	}
	list, err := s.an.Tokens(r.Context(), user)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	reply(w, list)
}

// mintToken answers POST /api/tokens {name, ttl}: a token of the caller, its secret in the answer — once.
func (s *Server) mintToken(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.userAuth(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
		TTL  string `json:"ttl,omitempty"` // "720h"; empty — no expiry
	}
	if err := decode(r, &body); err != nil || len(body.Name) == 0 {
		fail(w, http.StatusBadRequest, codeBadRequest, "the body is {\"name\": \"ci\"}")
		return
	}
	var ttl time.Duration
	if len(body.TTL) > 0 {
		var err error
		if ttl, err = time.ParseDuration(body.TTL); err != nil {
			fail(w, http.StatusBadRequest, codeBadRequest, "ttl as \"720h\": %v", err)
			return
		}
	}
	tok, secret, err := s.an.NewToken(r.Context(), body.Name, user, ttl)
	if err != nil {
		fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
		return
	}
	reply(w, map[string]any{"token": tok, "secret": secret})
}

// dropToken answers DELETE /api/tokens/{id}: the caller's token revoked.
func (s *Server) dropToken(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.userAuth(w, r)
	if !ok {
		return
	}
	if err := s.an.DropToken(r.Context(), r.PathValue("id"), user); err != nil {
		fail(w, http.StatusNotFound, codeNotFound, "%v", err)
		return
	}
	reply(w, map[string]string{"dropped": r.PathValue("id")})
}
