package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/horchestra/pack/pkg/logging"

	"github.com/horchestra/pack-operator/bus"
	"github.com/horchestra/pack-operator/config"
	"github.com/horchestra/pack-operator/db"
	"github.com/horchestra/pack-operator/git"
	"github.com/horchestra/pack-operator/packbin"
	"github.com/horchestra/pack-operator/worker"
)

// WorkerOptions is the worker subcommand's own settings: the runtime it answers and the runtime's settings.
type WorkerOptions struct {
	Runtime    string            // native | docker | k8s; native by default
	Labels     map[string]string // the label set the worker answers (exact match with a job's)
	Image      string            // docker and k8s: the pack image (A7)
	Kubeconfig string            // k8s: the worker's own kubeconfig; empty — in-cluster
	Namespace  string            // k8s: the namespace the Jobs run in
	GitImage   string            // k8s: the init container's image
}

// Worker is an executor of the operator's jobs (B6): it opens the database the jobs come from, checks the pack
// binary, and runs the jobs of its runtime and label set until ctx ends.
func Worker(ctx context.Context, cfg *config.Config, opts WorkerOptions) error {
	log := logging.For("worker")
	runtime := opts.Runtime
	if len(runtime) == 0 {
		runtime = "native"
	}
	d, err := db.Open(ctx, cfg.DB)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer d.Close()
	if _, err := packbin.Version(ctx, cfg.Pack); err != nil {
		return err
	}
	var creds git.Store
	switch cfg.Secrets.Provider {
	case "openbao":
		creds = git.OpenBao{Addr: cfg.Secrets.Addr, Mount: cfg.Secrets.Mount, TokenEnv: cfg.Secrets.TokenEnv}
	default:
		if len(cfg.EncryptionKeyEnv) > 0 {
			if creds, err = git.NewDBSecrets(d, cfg.EncryptionKeyEnv); err != nil {
				return fmt.Errorf("the git credentials store: %w", err)
			}
		}
	}
	dataDir := cfg.DataDir
	if len(dataDir) == 0 {
		cache, _ := os.UserCacheDir()
		dataDir = filepath.Join(cache, "pack-operator")
	}
	var rt worker.Runtime
	switch runtime {
	case "native":
		rt = worker.Native{}
	case "docker":
		image := opts.Image
		if len(image) == 0 {
			image = "pack:latest"
		}
		rt = &worker.Docker{Image: image}
	case "k8s":
		rt = &worker.K8s{Kubeconfig: opts.Kubeconfig, Namespace: opts.Namespace, Image: opts.Image, GitImage: opts.GitImage}
	default:
		return fmt.Errorf("runtime %q: native, docker or k8s", runtime)
	}
	w := worker.New(cfg, bus.NewSQL(d), d, creds, git.NewRepos(filepath.Join(dataDir, "repos"), creds), runtime, opts.Labels, rt)
	log.Info().Str("runtime", runtime).Str("labels", labelsString(opts.Labels)).Msg("taking jobs")
	return w.Run(ctx)
}

// labelsString is a label set for the log line.
func labelsString(labels map[string]string) string {
	if len(labels) == 0 {
		return "any"
	}
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}
