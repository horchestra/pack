// Package server is the operator itself: serve wires the configuration, the database, the policy and the pack
// binary together and answers the API (B1 brings the skeleton — /api/version; B7 the rest).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/logging"

	"github.com/horchestra/pack-operator/authz"
	"github.com/horchestra/pack-operator/bus"
	"github.com/horchestra/pack-operator/config"
	"github.com/horchestra/pack-operator/db"
	"github.com/horchestra/pack-operator/git"
	"github.com/horchestra/pack-operator/packbin"
	"github.com/horchestra/pack-operator/policy"
	"github.com/horchestra/pack-operator/sync"
)

// Version is the operator's own version; a build stamps it.
var Version = "dev"

// Server is a running operator.
type Server struct {
	cfg     *config.Config
	db      *db.DB
	az      *authz.Authz
	bus     *bus.SQLBus
	syncer  *sync.Syncer
	pol     *policy.Policy
	pck     api.VersionResult
	dataDir string
	state   *stateStore // the pack state store, read only (К2)
	creds   git.Store   // the operator's secrets (git credentials first)
}

// Start opens everything the configuration names — the database migrated, the policy, the projects seeded, the
// pack binary checked — and serves on ln until ctx ends. A start that cannot keep its promises (no database, no
// pack) is an error, not a half-alive process.
func Start(ctx context.Context, cfg *config.Config, ln net.Listener) error {
	s, err := NewServer(ctx, cfg)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Serve(ctx, ln)
}

// NewServer opens everything the configuration names — the database migrated, the policy, the projects seeded,
// the pack binary checked. A start that cannot keep its promises (no database, no pack) is an error, not a
// half-alive process. Serve runs it; Close gives its ground back.
func NewServer(ctx context.Context, cfg *config.Config) (*Server, error) {
	log := logging.For("operator")
	if cfg.Auth == "none" {
		log.Warn().Str("user", cfg.DevUser).
			Msg("auth: none — every call is " + cfg.DevUser + ", an admin in every domain; only where nothing else is possible")
	}
	s := &Server{cfg: cfg}
	var err error
	if s.db, err = db.Open(ctx, cfg.DB); err != nil {
		return nil, fmt.Errorf("db: %w", err)
	}
	if s.az, err = authz.New(s.db, cfg.Auth, cfg.DevUser); err != nil {
		return nil, fmt.Errorf("authz: %w", err)
	}
	if err := s.loadProjects(ctx); err != nil {
		return nil, fmt.Errorf("the database's projects: %w", err)
	}
	for _, p := range s.cfg.Projects {
		if err := s.az.SeedProject(p.ID); err != nil {
			return nil, fmt.Errorf("project %s: %w", p.ID, err)
		}
	}
	if s.pck, err = packbin.Version(ctx, cfg.Pack); err != nil {
		return nil, err
	}
	s.bus = bus.NewSQL(s.db)
	var creds git.Store
	switch cfg.Secrets.Provider {
	case "openbao":
		creds = git.OpenBao{Addr: cfg.Secrets.Addr, Mount: cfg.Secrets.Mount, TokenEnv: cfg.Secrets.TokenEnv}
	default:
		if len(cfg.EncryptionKeyEnv) > 0 {
			if creds, err = git.NewDBSecrets(s.db, cfg.EncryptionKeyEnv); err != nil {
				return nil, fmt.Errorf("the git credentials store: %w", err)
			}
		}
	}
	dataDir := cfg.DataDir
	if len(dataDir) == 0 {
		cache, _ := os.UserCacheDir()
		dataDir = filepath.Join(cache, "pack-operator")
	}
	s.dataDir = dataDir
	s.creds = creds
	if s.state, err = openState(ctx, cfg.StateDSN); err != nil {
		return nil, fmt.Errorf("the state store: %w", err)
	}
	s.pol = policy.New(cfg, s.bus, s.db, s.az, nil)
	s.syncer = sync.New(cfg, s.bus, s.db, creds, filepath.Join(dataDir, "repos"), s.pol)
	s.pol.SetGit(s.syncer)
	return s, nil
}

// Serve runs the synchronizer, the policy and the API on ln until ctx ends.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() { _ = s.syncer.Run(ctx) }()
	go func() { _ = s.pol.Run(ctx) }()
	logging.For("operator").Info().Str("pack", s.pck.Version).Str("db", s.db.Driver()).
		Int("projects", len(s.cfg.Projects)).Str("addr", ln.Addr().String()).Msg("serving")
	mux := http.NewServeMux()
	s.routes(mux)
	hs := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(shut)
	}()
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Close gives the server's ground back: the state store, the synchronizer, the database.
func (s *Server) Close() {
	s.state.Close()
	s.syncer.Close()
	s.db.Close()
}

// Bus is the server's Bus (a worker of the same process shares it).
func (s *Server) Bus() *bus.SQLBus { return s.bus }

// version answers GET /api/version: the operator's and the pack binary's (К4).
func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	reply(w, struct {
		Operator string            `json:"operator"`
		Pack     api.VersionResult `json:"pack"`
	}{Version, s.pck})
}

// askSync answers POST /api/sync (К4): {repo, ref} — fetch the project now, in the background; 202.
func (s *Server) askSync(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo string `json:"repo"`
		Ref  string `json:"ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Repo) == 0 {
		reply(w, api.Failure{Error: api.Error{Code: "bad_request", Message: "the body is {\"repo\": \"<project>\", \"ref\": \"<branch>\"}"}})
		return
	}
	known := false
	for _, p := range s.cfg.Projects {
		known = known || p.ID == body.Repo
	}
	if !known {
		reply(w, api.Failure{Error: api.Error{Code: "not_found", Message: "no project " + body.Repo}})
		return
	}
	go func() {
		if err := s.syncer.Sync(context.Background(), body.Repo); err != nil {
			logging.For("operator").Warn().Err(err).Str("project", body.Repo).Msg("the asked sync")
		}
	}()
	w.WriteHeader(http.StatusAccepted)
	reply(w, struct {
		Sync string `json:"sync"`
	}{"started"})
}

// head is the last sha the synchronizer processed on the project's branch.
func (s *Server) head(ctx context.Context, projectID string) (string, error) {
	sha, err := s.syncer.Head(ctx, projectID)
	if err != nil {
		return "", err
	}
	if len(sha) == 0 {
		return "", fmt.Errorf("no sync of %s yet: POST /api/sync first", projectID)
	}
	return sha, nil
}

// reply writes v as the JSON document of an answer; an error as pkg/api.Failure's shape.
func reply(w http.ResponseWriter, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		b, _ = json.Marshal(api.Failure{Error: api.Error{Code: api.CodeError, Message: err.Error()}})
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(append(b, '\n'))
}
