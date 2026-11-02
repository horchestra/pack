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
	rollbackCmd.Flags().BoolVar(&rollbackDry, "dry-run", false, "say what would be done, do nothing")
	rootCmd.AddCommand(rollbackCmd)
}

var rollbackDry bool

var rollbackCmd = &cobra.Command{
	Use:   "rollback",
	Short: "take an installation back: undo an unfinished saga, or a saga back to the version replaced",
	Args:  cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		s, err := open(ctx, true)
		if err != nil {
			return err
		}
		defer s.Close()
		rec, curB, curRel, err := s.Installed()
		if err != nil {
			return err
		}
		if rec == nil || (len(rec.Pack) == 0 && !rec.Unfinished()) {
			return fmt.Errorf("nothing is installed on %s", s.Target)
		}
		if rec.Unfinished() {
			sg := rec.Saga
			_, toRel, err := s.Version(sg.Installation)
			if err != nil {
				return fmt.Errorf("the saga's version %s: %w", sg.Pack, err)
			}
			g, err := graph.New(adapter.Specs(toRel, nil))
			if err != nil {
				return err
			}
			fmt.Printf("undoing the saga towards %s %s (%s): in effect %v, under way %v\n", sg.Pack, sg.Version, rec.State, sg.Done, sg.Running)
			if rollbackDry {
				return nil
			}
			if err := s.Undo(ctx, g, curRel, toRel); err != nil {
				return err
			}
			if len(rec.Pack) == 0 {
				fmt.Println("not installed any more")
			} else {
				fmt.Printf("back to %s %s\n", rec.Pack, rec.Version)
			}
			return nil
		}

		prev := rec.Previous
		if prev == nil {
			return fmt.Errorf("no previous version is recorded — nothing to roll back to")
		}
		prevB, prevRel, err := s.Version(*prev)
		if err != nil {
			return fmt.Errorf("the previous version %s: %w", prev.Pack, err)
		}
		p, err := plan.Build(plan.Input{From: curB, To: prevB, FromOut: curRel, ToOut: prevRel})
		if err != nil {
			return err
		}
		fmt.Print(p.Text())
		if rollbackDry {
			return nil
		}
		if err := s.RollForward(ctx, p, curRel, prevRel, *prev); err != nil {
			return err
		}
		fmt.Printf("back to %s %s\n", prev.Pack, prev.Version)
		return nil
	}),
}
