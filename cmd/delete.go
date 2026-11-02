package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/pkg/adapter"
	"github.com/horchestra/pack/pkg/graph"
)

func init() {
	deleteCmd.Flags().BoolVar(&deleteDry, "dry-run", false, "say what would be done, do nothing")
	rootCmd.AddCommand(deleteCmd)
}

var deleteDry bool

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
		rec, _, rel, err := s.Installed()
		if err != nil {
			return err
		}
		if rec == nil || len(rec.Pack) == 0 {
			return fmt.Errorf("nothing is installed on %s", s.Target)
		}
		if rec.Unfinished() {
			return fmt.Errorf("a saga towards %s did not finish (%s: %s): apply it again, or pack rollback", rec.Saga.Pack, rec.State, rec.Saga.Error)
		}
		g, err := graph.New(adapter.Specs(rel, nil))
		if err != nil {
			return err
		}
		for _, n := range g.Reverse() {
			fmt.Println("remove", n.Name)
		}
		if deleteDry {
			return nil
		}
		if err := s.Remove(ctx, g, rel); err != nil {
			return err
		}
		fmt.Println("not installed any more")
		return nil
	}),
}
