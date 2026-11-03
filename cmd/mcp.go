package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/mcpserver"
)

func init() { rootCmd.AddCommand(mcpCmd) }

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "an MCP server on stdio for a model that runs the release: next, questions, answer, check, plan, apply, diagnose, rollback, status, run",
	Long: `Serves the release of the current directory (-C) to a model over MCP on stdin and stdout — a local
one (Ollama, llama.cpp) as much as any. Each tool runs pack as its command does, with --json,
and answers with what --json says. What is a person's to give is not a tool's: no --approve,
no secret's value — a password is a reference to a secret.`,
	Annotations: map[string]string{makesRelease: "true"}, // next says new where there is no release yet
	Args:        cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		if jsonOut {
			// --json makes stdout the result's and the rest stderr: MCP would speak on stderr, to no one
			return errors.New("mcp speaks JSON-RPC on stdout itself: no --json")
		}
		ctx, stop := interrupt.Context()
		defer stop()
		self, err := os.Executable()
		if err != nil {
			return err
		}
		wd, err := filepath.Abs(".")
		if err != nil {
			return err
		}
		s := &mcpserver.Server{Pack: self, Dir: wd, Env: os.Environ(), Envname: envName}
		return mcpserver.New(s, buildVersion()).Run(ctx, &mcp.StdioTransport{})
	}),
}

// buildVersion is pack's version as the build says it: the module's — "(devel)" for a go build of the tree — or
// "unknown" for a binary with no build info.
func buildVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok && len(bi.Main.Version) > 0 {
		return bi.Main.Version
	}
	return "unknown"
}
