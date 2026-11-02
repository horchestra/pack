// Package cmd is the command line: each command reads the release it runs in and hands the work to internal/.
package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/internal/workdir"
)

var rootCmd = &cobra.Command{
	Use:   "pack",
	Short: "the package format, and the tool that renders and installs it",
	Long: `pack checks, renders, plans and installs a pack — from a directory or a registry
— on a target, through the target's provider.

Registries: release.yaml names the variables that hold the credentials (username_env, password_env).`,
	// A command that fails says why and exits (see run): its error is not a misuse, and no usage follows it.
	// What cobra itself refuses — an unknown flag or command, arguments that are not expected — keeps its usage.
	SilenceErrors: true,
}

var (
	chdir   string
	envName string
	// dir is the directory of this run; set before any command runs.
	dir *workdir.Dir
)

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVarP(&chdir, "chdir", "C", "", "run in this directory, as if started there (a release: release.yaml, values, env.yaml)")
	pf.StringVar(&envName, "env", os.Getenv("PACK_ENV"), "the environment: env.<name>.yaml of the release instead of env.yaml (PACK_ENV)")
	rootCmd.PersistentPreRun = run(func(*cobra.Command, []string) error {
		if len(chdir) > 0 {
			if err := os.Chdir(chdir); err != nil {
				return err
			}
		}
		rel, err := release.Load(".", envName)
		if err != nil {
			return err
		}
		dir = workdir.New(rel, envName)
		dir.Ensure()
		return nil
	})
}

// Execute runs the command line.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fail(err)
	}
}

// run is the Run of a command whose work can fail: the error is said, as "pack: …", and pack exits with 1. The
// function returns first, so its deferred calls (the providers stopped, the lock released) have run.
func run(f func(*cobra.Command, []string) error) func(*cobra.Command, []string) {
	return func(c *cobra.Command, args []string) {
		if err := f(c, args); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "pack:", err)
	os.Exit(1)
}

// open is the session of a command: readOnly — the secret store is only read.
func open(ctx context.Context, readOnly bool) (*session.Session, error) {
	return session.Open(ctx, dir, readOnly)
}
