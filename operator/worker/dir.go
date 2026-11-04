package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack-operator/bus"
	"github.com/horchestra/pack-operator/config"
)

// jobDir is the job's temporary directory (К6): checkout/ — the tree of the request's sha, home/ — the job's
// HOME. The caller removes it after the job.
func (w *Worker) jobDir(ctx context.Context, proj *config.Project, req bus.Request) (string, error) {
	base := filepath.Join(w.cfg.DataDir, "jobs")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(base, "job-")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(dir, "home"), 0o700); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	if err := os.MkdirAll(w.cacheDir(proj), 0o755); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	if err := w.repos.Checkout(ctx, proj.ID, proj.Repo, proj.Credentials, req.SHA, filepath.Join(dir, "checkout")); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("the checkout of %s: %w", req.SHA, err)
	}
	return dir, nil
}

// jobEnv is the job's environment (К1): variables only, never argv. The names the release's release.yaml
// gives — state_dsn_env, the registry's username_env/password_env — are honored; the values come from the
// operator's configuration (the state DSN) and from the worker's own environment (the registry's, the store
// token): the worker's environment is where the deployer puts them.
func (w *Worker) jobEnv(proj *config.Project, req bus.Request, dir string) ([]string, error) {
	env := []string{
		"HOME=" + filepath.Join(dir, "home"),
		"PACK_CACHE=" + w.cacheDir(proj),
		"PACK_REVISION=" + req.SHA,
		"PACK_LOG_FORMAT=json",
		"PATH=" + os.Getenv("PATH"),
	}
	var rel struct {
		StateDSNEnv string `json:"state_dsn_env"`
		Registry    struct {
			UsernameEnv string `json:"username_env"`
			PasswordEnv string `json:"password_env"`
		} `json:"registry"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "checkout", filepath.FromSlash(req.Target.Release), "release.yaml"))
	if err == nil {
		if err := yaml.Unmarshal(b, &rel); err != nil {
			return nil, fmt.Errorf("%s: %w", req.Target.Release, err)
		}
	}
	dsnEnv := rel.StateDSNEnv
	if len(dsnEnv) == 0 {
		dsnEnv = "PACK_STATE_DSN"
	}
	if len(w.cfg.StateDSN) > 0 {
		env = append(env, dsnEnv+"="+w.cfg.StateDSN)
	}
	for _, name := range []string{rel.Registry.UsernameEnv, rel.Registry.PasswordEnv, "PACK_STORE_TOKEN"} {
		if len(name) > 0 {
			if v, ok := os.LookupEnv(name); ok {
				env = append(env, name+"="+v)
			}
		}
	}
	return env, nil
}

// envFile writes the environment as a --env-file (the docker runtime): KEY=value a line, ' quotes.
func envFile(dir string, env []string) (string, error) {
	p := filepath.Join(dir, "env")
	var out strings.Builder
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		fmt.Fprintf(&out, "%s=%s\n", k, v)
	}
	if err := os.WriteFile(p, []byte(out.String()), 0o600); err != nil {
		return "", err
	}
	return p, nil
}
