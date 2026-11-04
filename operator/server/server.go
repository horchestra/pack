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
	"time"

	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/logging"

	"github.com/horchestra/pack-operator/authz"
	"github.com/horchestra/pack-operator/config"
	"github.com/horchestra/pack-operator/db"
	"github.com/horchestra/pack-operator/packbin"
)

// Version is the operator's own version; a build stamps it.
var Version = "dev"

// Server is a running operator.
type Server struct {
	cfg *config.Config
	db  *db.DB
	az  *authz.Authz
	pck api.VersionResult
}

// Start opens everything the configuration names — the database migrated, the policy, the projects seeded, the
// pack binary checked — and serves on ln until ctx ends. A start that cannot keep its promises (no database, no
// pack) is an error, not a half-alive process.
func Start(ctx context.Context, cfg *config.Config, ln net.Listener) error {
	log := logging.For("operator")
	if cfg.Auth == "none" {
		log.Warn().Str("user", cfg.DevUser).
			Msg("auth: none — every call is " + cfg.DevUser + ", an admin in every domain; only where nothing else is possible")
	}
	s := &Server{cfg: cfg}
	var err error
	if s.db, err = db.Open(ctx, cfg.DB); err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer s.db.Close()
	if s.az, err = authz.New(s.db, cfg.Auth, cfg.DevUser); err != nil {
		return fmt.Errorf("authz: %w", err)
	}
	for _, p := range cfg.Projects {
		if err := s.az.SeedProject(p.ID); err != nil {
			return fmt.Errorf("project %s: %w", p.ID, err)
		}
	}
	if s.pck, err = packbin.Version(ctx, cfg.Pack); err != nil {
		return err
	}
	log.Info().Str("pack", s.pck.Version).Str("db", s.db.Driver()).Int("projects", len(cfg.Projects)).
		Str("addr", ln.Addr().String()).Msg("serving")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", s.version)
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

// version answers GET /api/version: the operator's and the pack binary's (К4).
func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	reply(w, struct {
		Operator string            `json:"operator"`
		Pack     api.VersionResult `json:"pack"`
	}{Version, s.pck})
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
