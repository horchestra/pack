// pack-operator runs the releases of the projects' repositories: serve is the operator (API, sync, policy),
// worker an executor of its jobs (B6).
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/pkg/logging"

	"github.com/horchestra/pack-operator/config"
	"github.com/horchestra/pack-operator/server"
)

var configPath string

var root = &cobra.Command{
	Use:          "pack-operator",
	Short:        "runs the releases of the projects' repositories",
	SilenceUsage: true,
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "the operator: the API, the git sync, the policy",
	Args:  cobra.NoArgs,
	RunE: func(*cobra.Command, []string) error {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		ln, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			return fmt.Errorf("listen %s: %w", cfg.Listen, err)
		}
		return server.Start(interruptContext(), cfg, ln)
	},
}

var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "an executor of the operator's jobs",
	Args:  cobra.NoArgs,
	RunE: func(*cobra.Command, []string) error {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		return server.Worker(interruptContext(), cfg)
	},
}

func init() {
	root.PersistentFlags().StringVarP(&configPath, "config", "f", envOr("PACK_OPERATOR_CONFIG", "operator.yaml"),
		"the configuration file (PACK_OPERATOR_CONFIG)")
	root.AddCommand(serveCmd, workerCmd)
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); len(v) > 0 {
		return v
	}
	return fallback
}

// interruptContext ends on SIGINT or SIGTERM, as pack's jobs do (К1).
func interruptContext() context.Context {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() { <-ctx.Done(); stop() }()
	return ctx
}

func main() {
	if err := logging.Setup(logging.FromEnv(), os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "pack-operator:", err)
		os.Exit(1)
	}
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "pack-operator:", err)
		os.Exit(1)
	}
}
