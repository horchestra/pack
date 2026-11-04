package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/pkg/api"

	"github.com/horchestra/pack-operator/config"
)

// packOp is a synchronous operation of К4: the checkout of the sha (the query's, HEAD's by default) in a
// temporary directory, pack -C <release> <op> --json with a timeout (60 s by default, ?timeout= to change),
// the JSON document as the answer — no queue, no job.
func (s *Server) packOp(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, proj, rel, ok := s.releaseAccess(w, r, "read")
		if !ok {
			return
		}
		timeout := 60 * time.Second
		if v := r.URL.Query().Get("timeout"); len(v) > 0 {
			if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
				timeout = time.Duration(secs) * time.Second
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		doc, err := s.runPackOp(ctx, proj, rel, r.URL.Query().Get("sha"), op, r)
		if errors.Is(err, errPackFailed) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write(append(doc, '\n'))
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, api.CodeError, "%v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(append(doc, '\n'))
	}
}

// runPackOp does the checkout and the run of one synchronous operation.
func (s *Server) runPackOp(ctx context.Context, proj *config.Project, rel, sha, op string, r *http.Request) ([]byte, error) {
	if len(sha) == 0 {
		var err error
		if sha, err = s.head(ctx, proj.ID); err != nil {
			return nil, fmt.Errorf("the HEAD of %s: %w", proj.ID, err)
		}
	}
	dir, err := os.MkdirTemp("", "pack-operator-op-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := s.syncer.Checkout(ctx, proj.ID, sha, filepath.Join(dir, "checkout")); err != nil {
		return nil, fmt.Errorf("the checkout of %s: %w", sha, err)
	}
	checkout := filepath.Join(dir, "checkout")
	env, err := s.opEnv(proj, rel, checkout, dir)
	if err != nil {
		return nil, err
	}
	args := []string{"-C", rel}
	// schema and explain are the values' (pack values schema|explain), the API keeps the short names (К4)
	if op == "schema" || op == "explain" {
		args = append(args, "values")
	}
	args = append(args, op, "--json")
	switch op {
	case "logs":
		if key := r.URL.Query().Get("key"); len(key) > 0 {
			args = append(args, "--key", key)
		}
		if tail := r.URL.Query().Get("tail"); len(tail) > 0 {
			args = append(args, "--tail", tail)
		}
	case "schema", "explain":
		if ptr := r.URL.Query().Get("ptr"); len(ptr) > 0 {
			args = append(args, ptr)
		}
		if lang := r.URL.Query().Get("lang"); len(lang) > 0 {
			args = append(args, "--lang", lang)
		}
	}
	cmd := exec.CommandContext(ctx, s.cfg.Pack, args...)
	cmd.Dir = checkout
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%s %s: over %s", op, rel, r.URL.Query().Get("timeout"))
		}
		doc := bytes.TrimSpace(stdout.Bytes())
		var failure api.Failure
		if json.Unmarshal(doc, &failure) == nil && len(failure.Error.Code) > 0 {
			return doc, errPackFailed // pack's own document says why (nothing installed yet, …)
		}
		return nil, fmt.Errorf("pack %s: %w: %s", op, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return bytes.TrimSpace(stdout.Bytes()), nil
}

// errPackFailed marks a pack exit whose stdout already holds the Failure document: the API answers it as it is.
var errPackFailed = errors.New("pack failed")

// opEnv is a synchronous operation's environment (К1, as a job's): HOME and PACK_CACHE of the operator's own,
// the state DSN under the name the release.yaml gives.
func (s *Server) opEnv(proj *config.Project, rel, checkout, dir string) ([]string, error) {
	cache := filepath.Join(s.dataDir, "cache", proj.ID)
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "home"), 0o700); err != nil {
		return nil, err
	}
	env := []string{
		"HOME=" + filepath.Join(dir, "home"),
		"PACK_CACHE=" + cache,
		"PACK_LOG_FORMAT=json",
		"PATH=" + os.Getenv("PATH"),
	}
	var relDoc struct {
		StateDSNEnv string `json:"state_dsn_env"`
	}
	b, err := os.ReadFile(filepath.Join(checkout, filepath.FromSlash(rel), "release.yaml"))
	if err == nil {
		if err := yaml.Unmarshal(b, &relDoc); err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
	}
	dsnEnv := relDoc.StateDSNEnv
	if len(dsnEnv) == 0 {
		dsnEnv = "PACK_STATE_DSN"
	}
	if len(s.cfg.StateDSN) > 0 {
		env = append(env, dsnEnv+"="+s.cfg.StateDSN)
	}
	return env, nil
}
