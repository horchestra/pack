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
	"github.com/horchestra/pack/pkg/plan"
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
	Refresh     bool
	Env         map[string]string
	Hear        func(saga.Entry)
	// Planned, when set, is called with the approved plan before the saga runs: it says why every key is
	// applied at all ("added", "config only", "drift") — what a repeat apply was expected to skip.
	Planned func(*plan.Plan)
}

var embeddedApplyMu sync.Mutex

// Apply runs the same prepare, plan and saga as the apply command in the
// current process. Refresh brings the release's external targets first.
// Embedded applies are serialized because cache and credential settings are
// environment variables shared by the process.
func Apply(ctx context.Context, options ApplyOptions) error {
	embeddedApplyMu.Lock()
	defer embeddedApplyMu.Unlock()
	if options.Dir == "" {
		options.Dir = "."
	}
	set := map[string]string{}
	if options.CacheDir != "" {
		set["PACK_CACHE"] = options.CacheDir
	}
	for name, value := range options.Env {
		set[name] = value
	}
	old := map[string]string{}
	existed := map[string]bool{}
	restore := func() {
		for name := range set {
			if existed[name] {
				_ = os.Setenv(name, old[name])
			} else {
				_ = os.Unsetenv(name)
			}
		}
	}
	for name, value := range set {
		old[name], existed[name] = os.LookupEnv(name)
		if err := os.Setenv(name, value); err != nil {
			restore()
			return err
		}
	}
	defer restore()
	rel, err := release.Load(options.Dir, options.Environment)
	if err != nil {
		return err
	}
	if options.Refresh {
		if _, _, err := refreshRelease(ctx, rel, options.Environment, false); err != nil {
			return err
		}
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
	if options.Planned != nil {
		options.Planned(change.Plan)
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
