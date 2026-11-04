// Package cmd is the command line: each command reads the release it runs in and hands the work to internal/.
package cmd

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/logging"
	"github.com/horchestra/pack/pkg/saga"
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
	// logLevel and logFormat are the log's (--log-level, --log-format); commandStart when the command started.
	logLevel, logFormat string
	commandStart        time.Time

	// dir is the directory of this run; set before any command runs.
	dir *workdir.Dir
)

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVarP(&chdir, "chdir", "C", "", "run in this directory, as if started there (a release: release.yaml, values, env.yaml)")
	pf.StringVar(&envName, "env", os.Getenv("PACK_ENV"), "the environment: env.<name>.yaml of the release instead of env.yaml (PACK_ENV)")
	pf.StringVar(&logLevel, "log-level", os.Getenv(logging.EnvLevel), "the log on stderr: "+strings.Join(logging.Levels, ", ")+" ("+logging.EnvLevel+"; "+logging.DefaultLevel+" by default); the targets log at it too")
	pf.StringVar(&logFormat, "log-format", os.Getenv(logging.EnvFormat), "the log's format: console (a person's) or json ("+logging.EnvFormat+")")
	pf.BoolVar(&jsonOut, "json", false, "the result as one JSON document on stdout, what is said along the way on stderr; an error as {\"error\": {code, message}, \"next\": […]}")
	rootCmd.PersistentPostRun = func(c *cobra.Command, _ []string) {
		logging.For("cmd").Debug().Str("command", c.CommandPath()).Dur("took", time.Since(commandStart)).Msg("done")
		if f, ok := stdout.(*os.File); ok && jsonOut {
			os.Stdout = f // the next command of a process (a test's) starts as it was
		}
		if streamOut {
			stopStream()
		}
	}
	rootCmd.PersistentPreRun = run(func(c *cobra.Command, args []string) error {
		if err := logging.Setup(logging.Config{Level: logLevel, Format: logFormat}, os.Stderr); err != nil {
			return err
		}
		commandStart = time.Now()
		logging.For("cmd").Debug().Str("command", c.CommandPath()).Strs("args", args).Str("version", c.Root().Version).Msg("start")
		if jsonOut {
			asJSON()
		}
		if streamOut {
			if err := startStream(); err != nil {
				return err
			}
		}
		if len(chdir) > 0 {
			if err := os.Chdir(chdir); err != nil {
				return err
			}
		}
		if c.Annotations[makesRelease] == "true" {
			return nil // it reads, and makes, its release itself: none is here yet, and no .pack/ is made in passing
		}
		rel, err := release.Load(".", envName)
		if err != nil {
			return err
		}
		logging.For("cmd").Debug().Str("dir", mustGetwd()).Str("env", envName).Str("pack", rel.Pack).Msg("release")
		dir = workdir.New(rel, envName)
		dir.Ensure()
		return nil
	})
}

func mustGetwd() string {
	wd, _ := os.Getwd()
	return wd
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

// open is the session of a command: readOnly — the secret store is only read.
func open(ctx context.Context, readOnly bool) (*session.Session, error) {
	s, err := session.Open(ctx, dir, readOnly)
	if err == nil && streamOut {
		s.Hear = func(e saga.Entry) { streamEvent(entryEvent(e)) }
	}
	return s, err
}
