package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/pkg/adapter"
	"github.com/horchestra/pack/pkg/graph"
	"github.com/horchestra/pack/pkg/plan"
)

func init() {
	deleteCmd.Flags().BoolVar(&deleteDry, "dry-run", false, "say what would be done, do nothing")
	deleteCmd.Flags().StringVar(&deleteApprove, "approve", "", "take the installation away, as --dry-run says it by this digest, whatever run.approve says")
	rootCmd.AddCommand(deleteCmd)
}

var (
	deleteDry     bool
	deleteApprove string
)

var deleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "remove an installation: the workloads go, the data and credentials stay",
	Args:  cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		s, err := open(ctx, true)
		if err != nil {
			return err
		}
		defer s.Close()
		rec, b, rel, err := s.Installed()
		if err != nil {
			return err
		}
		if rec == nil || len(rec.Pack) == 0 {
			return withCode(CodeNothingInstalled, fmt.Errorf("nothing is installed on %s", s.Target))
		}
		if rec.Unfinished() {
			return withCode(CodeSagaUnfinished, fmt.Errorf("a saga towards %s did not finish (%s: %s): apply it again, or pack rollback", rec.Saga.Pack, rec.State, rec.Saga.Error))
		}
		g, err := graph.New(adapter.Specs(rel, nil))
		if err != nil {
			return err
		}
		var keys []string
		for _, n := range g.Reverse() {
			fmt.Println("remove", n.Name)
			keys = append(keys, n.Name)
		}
		// a removal is a plan of its own: what goes, by its digest
		p := &plan.Plan{Pack: rec.Pack, From: rec.Version, Direction: plan.DirUninstall, Order: keys}
		plan.Seal(p, rel, nil)
		fmt.Println("plan", p.Digest)
		planned(p)
		removed := struct {
			Removed version  `json:"removed"`
			Keys    []string `json:"keys"`
			Digest  string   `json:"digest"`
			DryRun  bool     `json:"dry_run,omitempty"`
		}{versionOf(b.Name, rec.Version, rec.Pack), keys, p.Digest, deleteDry}
		if deleteDry {
			return result(removed)
		}
		if err := approved(s, p, deleteApprove, howDelete); err != nil {
			return err
		}
		if err := s.Remove(ctx, g, rel); err != nil {
			return withCode(CodeSagaFailed, err)
		}
		fmt.Println("not installed any more")
		return result(removed)
	}),
}
