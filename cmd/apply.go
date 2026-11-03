package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/pkg/plan"
)

func init() { rootCmd.AddCommand(applyCmd) }

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
		if err := s.Apply(ctx, c); err != nil {
			return withCode(CodeSagaFailed, err)
		}
		fmt.Println(strings.Join(strings.Fields(fmt.Sprintf("installed %s %s on %s (%s)", c.ToB.Name, c.ToB.Version, s.Target, s.ID)), " "))
		return result(installedResult{Installed: versionOf(c.ToB.Name, c.ToB.Version, c.Ref), Target: s.Target, Installation: s.ID, Plan: c.Plan})
	}),
}

// installedResult is what apply and rollback did, as --json says it.
type installedResult struct {
	Installed    version    `json:"installed"`
	Target       string     `json:"target"`
	Installation string     `json:"installation"`
	Plan         *plan.Plan `json:"plan,omitempty"`
}

// version is a pack at a version, and where it is from.
type version struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Pack    string `json:"pack"`
}

func versionOf(name, v, ref string) version { return version{Name: name, Version: v, Pack: ref} }
