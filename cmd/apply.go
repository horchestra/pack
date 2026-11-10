package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/saga"
)

func init() {
	applyCmd.Flags().StringVar(&applyApprove, "approve", "", "apply the plan of this digest (pack plan says it), whatever run.approve says: the plan shown, and no other")
	rootCmd.AddCommand(applyCmd)
}

var applyApprove string

// ApplyOptions is an embedded apply of one release directory. The caller has
// already approved the change; unlike the CLI, Apply does not prompt.
type ApplyOptions struct {
	Dir         string
	Environment string
	CacheDir    string
	Hear        func(saga.Entry)
}

var embeddedApplyMu sync.Mutex

// Apply runs the same prepare, plan and saga as the apply command in the
// current process. Embedded applies are serialized because pack's cache is an
// environment setting shared by the process.
func Apply(ctx context.Context, options ApplyOptions) error {
	embeddedApplyMu.Lock()
	defer embeddedApplyMu.Unlock()
	if options.Dir == "" {
		options.Dir = "."
	}
	if options.CacheDir != "" {
		old, existed := os.LookupEnv("PACK_CACHE")
		if err := os.Setenv("PACK_CACHE", options.CacheDir); err != nil {
			return err
		}
		defer func() {
			if existed {
				_ = os.Setenv("PACK_CACHE", old)
			} else {
				_ = os.Unsetenv("PACK_CACHE")
			}
		}()
	}
	rel, err := release.Load(options.Dir, options.Environment)
	if err != nil {
		return err
	}
	dir := workdir.New(rel, options.Environment)
	dir.Path = filepath.Join(options.Dir, ".pack")
	dir.Ensure()
	s, err := session.Open(ctx, dir, false)
	if err != nil {
		return err
	}
	defer s.Close()
	s.Hear = options.Hear
	change, err := s.Prepare(true)
	if err != nil {
		return err
	}
	return s.Apply(ctx, change)
}

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "install or upgrade: the target says what is installed, pack drives the saga step by step",
	Long: `The target says what is installed, pack plans the saga and drives it step by step
through the provider, rolling a failed unit back. An unfinished saga continues from
its last savepoint. The target must perform steps.

The pack to install is the pack of release.yaml: a composition or component
directory, or a registry reference.`,
	Args: cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		s, err := open(ctx, false)
		if err != nil {
			return err
		}
		defer s.Close()
		c, err := s.Prepare(true)
		if err != nil {
			return err
		}
		fmt.Print(c.Plan.Text())
		planned(c.Plan)
		if err := approved(s, c.Plan, applyApprove, howApply); err != nil {
			return err
		}
		if err := s.Apply(ctx, c); err != nil {
			return withCode(api.CodeSagaFailed, err)
		}
		fmt.Println(strings.Join(strings.Fields(fmt.Sprintf("installed %s %s on %s (%s)", c.ToB.Name, c.ToB.Version, s.Target, s.ID)), " "))
		return result(api.InstalledResult{Installed: api.VersionOf(c.ToB.Name, c.ToB.Version, c.Ref), Target: s.Target, Installation: s.ID, Plan: c.Plan})
	}),
}
