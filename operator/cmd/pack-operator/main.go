// pack-operator runs the releases of the projects' repositories: serve is the operator (API, sync, policy),
// worker an executor of its jobs (B6).
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/horchestra/pack/pkg/logging"

	"github.com/horchestra/pack-operator/config"
	"github.com/horchestra/pack-operator/mcpserver"
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

var workerOpts server.WorkerOptions
var workerLabels []string

var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "an executor of the operator's jobs",
	Args:  cobra.NoArgs,
	RunE: func(*cobra.Command, []string) error {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		workerOpts.Labels = map[string]string{}
		for _, l := range workerLabels {
			k, v, ok := strings.Cut(l, "=")
			if !ok {
				return fmt.Errorf("a label is key=value: %q", l)
			}
			workerOpts.Labels[k] = v
		}
		if len(workerOpts.Labels) == 0 {
			workerOpts.Labels = nil
		}
		return server.Worker(interruptContext(), cfg, workerOpts)
	},
}

var mcpOperator, mcpUser string

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "an MCP server on stdio for a model that runs the operator's releases: next, check, plan, status, history, logs, journal, request, job",
	Long: `Serves the operator's API (-—operator) to a model over MCP on stdin and stdout. What is a person's
is not a tool's: no approval, no secret's value.`,
	Args: cobra.NoArgs,
	RunE: func(*cobra.Command, []string) error {
		srv := mcpserver.New(&mcpserver.Client{Base: mcpOperator, User: mcpUser}, server.Version)
		return srv.Run(interruptContext(), &mcp.StdioTransport{})
	},
}

func init() {
	root.PersistentFlags().StringVarP(&configPath, "config", "f", envOr("PACK_OPERATOR_CONFIG", "operator.yaml"),
		"the configuration file (PACK_OPERATOR_CONFIG)")
	workerCmd.Flags().StringVar(&workerOpts.Runtime, "runtime", "native", "the jobs' runtime: native | docker | k8s")
	workerCmd.Flags().StringArrayVar(&workerLabels, "label", nil, "a label of the set the worker answers (key=value, repeatable)")
	workerCmd.Flags().StringVar(&workerOpts.Image, "image", "", "docker and k8s: the pack image (\"pack:latest\" for docker by default)")
	workerCmd.Flags().StringVar(&workerOpts.Kubeconfig, "kubeconfig", "", "k8s: the worker's own kubeconfig; empty — in-cluster")
	workerCmd.Flags().StringVar(&workerOpts.Namespace, "namespace", "", "k8s: the namespace the Jobs run in")
	workerCmd.Flags().StringVar(&workerOpts.GitImage, "git-image", "", "k8s: the init container's image (\"alpine/git:latest\" by default)")
	mcpCmd.Flags().StringVar(&mcpOperator, "operator", envOr("PACK_OPERATOR_URL", "http://127.0.0.1:8080"),
		"the operator API's URL (PACK_OPERATOR_URL)")
	mcpCmd.Flags().StringVar(&mcpUser, "user", envOr("PACK_OPERATOR_USER", ""),
		"the X-Pack-User the calls are as (PACK_OPERATOR_USER); empty — the operator's own (auth: none)")
	root.AddCommand(serveCmd, workerCmd, mcpCmd)
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
