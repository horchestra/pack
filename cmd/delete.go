package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/adapter"
	"github.com/horchestra/pack/internal/graph"
	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/saga"
)

func init() {
	deleteCmd.Flags().BoolVar(&deleteDry, "dry-run", false, "say what would be done, do nothing")
	deleteCmd.Flags().StringVar(&deleteApprove, "approve", "", "take the installation away, as --dry-run says it by this digest, whatever run.approve says")
	deleteCmd.Flags().BoolVar(&deleteDestroy, "destroy", false, "take the keys' OS packages away too, by name, no autoremove — for a host the installation has to itself")
	rootCmd.AddCommand(deleteCmd)
}

var (
	deleteDry     bool
	deleteApprove string
	deleteDestroy bool
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
			// state: observe keeps no record: what the release wants rendered now is what there is to take away
			if s.Dir.Release.State != workdir.StateObserve {
				return withCode(api.CodeNothingInstalled, fmt.Errorf("nothing is installed on %s", s.Target))
			}
			if b, rel, err = s.Render(); err != nil {
				return err
			}
			rec = &saga.Record{Installation: saga.Installation{Pack: b.Name, Version: b.Version}}
		}
		if rec.Unfinished() {
			return withCode(api.CodeSagaUnfinished, fmt.Errorf("a saga towards %s did not finish (%s: %s): apply it again, or pack rollback", rec.Saga.Pack, rec.State, rec.Saga.Error))
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
		removed := api.RemovedResult{Removed: api.VersionOf(b.Name, rec.Version, rec.Pack), Keys: keys, Digest: p.Digest, DryRun: deleteDry}
		if deleteDry {
			return result(removed)
		}
		if err := approved(s, p, deleteApprove, howDelete); err != nil {
			return err
		}
		if err := s.Remove(ctx, g, rel, deleteDestroy); err != nil {
			return withCode(api.CodeSagaFailed, err)
		}
		fmt.Println("not installed any more")
		return result(removed)
	}),
}
