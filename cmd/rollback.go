package cmd

import (
	"context"
	"fmt"
	"slices"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/pkg/adapter"
	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/graph"
	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/saga"
)

func init() {
	rollbackCmd.Flags().BoolVar(&rollbackDry, "dry-run", false, "say what would be done, do nothing")
	rollbackCmd.Flags().StringVar(&rollbackApprove, "approve", "", "run the plan of this digest (--dry-run says it), whatever run.approve says")
	rollbackCmd.Flags().IntVar(&rollbackTo, "to", 0, "go back to this revision of pack history, its pack and its values; a revision that was not installed is refused")
	rootCmd.AddCommand(rollbackCmd)
}

var (
	rollbackDry     bool
	rollbackTo      int
	rollbackApprove string
)

var rollbackCmd = &cobra.Command{
	Use:   "rollback",
	Short: "take an installation back: undo an unfinished saga, or a saga back to the version replaced (--to: to a revision)",
	Long: `With no saga unfinished, runs a saga back to the version the last one replaced — or, with
--to <n>, to revision n of pack history: its pack (by digest) and the values it was installed
with, through the same saga as an apply, the hooks of the version transition with it; a
savepoint limits what is taken back as it does in apply. An unfinished saga is taken back
first (pack rollback, without --to).`,
	Args: cobra.NoArgs,
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
		if rec == nil || (len(rec.Pack) == 0 && !rec.Unfinished() && (rollbackTo == 0 || len(rec.History) == 0)) {
			return withCode(CodeNothingInstalled, fmt.Errorf("nothing is installed on %s", s.Target))
		}
		if rollbackTo != 0 {
			return rollbackToRevision(ctx, s, rec, curB, curRel, rollbackTo)
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
			// the undoing is a plan of its own, as a removal is: what is taken back, by its digest
			p := &plan.Plan{Pack: sg.Pack, From: sg.Version, To: rec.Version, Direction: plan.DirUndo,
				Order: append(slices.Clone(sg.Done), sg.Running...)}
			plan.Seal(p, nil, nil) // a taking back is recovery: no risk of its own, run.approve: plan asks as for any plan
			fmt.Println("plan", p.Digest)
			undone := struct {
				Undone version `json:"undone"`
				Back   version `json:"back"`
				Digest string  `json:"digest"`
				DryRun bool    `json:"dry_run,omitempty"`
			}{versionOf("", sg.Version, sg.Pack), versionOf("", rec.Version, rec.Pack), p.Digest, rollbackDry}
			if rollbackDry {
				return result(undone)
			}
			if err := approved(s, p, rollbackApprove, howRollback(0)); err != nil {
				return err
			}
			if err := s.Undo(ctx, g, curRel, toRel); err != nil {
				return withCode(CodeSagaFailed, err)
			}
			if len(rec.Pack) == 0 {
				fmt.Println("not installed any more")
			} else {
				fmt.Printf("back to %s %s\n", rec.Pack, rec.Version)
			}
			return result(undone)
		}

		if rec.Previous == nil {
			return fmt.Errorf("no previous version is recorded — nothing to roll back to")
		}
		return rollForwardTo(ctx, s, curB, curRel, *rec.Previous, "the previous version")
	}),
}

// rollbackToRevision runs a saga to revision n: what it installed, with its values.
func rollbackToRevision(ctx context.Context, s *session.Session, rec *saga.Record, curB *bundle.Bundle, curRel *render.Release, n int) error {
	if rec.Unfinished() {
		return fmt.Errorf("a saga towards %s %s is unfinished: pack rollback, without --to, takes it back first", rec.Saga.Pack, rec.Saga.Version)
	}
	rev := rec.Revision(n)
	if rev == nil {
		return fmt.Errorf("no revision %d in the history (pack history)", n)
	}
	if rev.Outcome != saga.RevInstalled {
		return fmt.Errorf("revision %d was %s, not installed: there is nothing of it to go back to", n, rev.Outcome)
	}
	if cur := rec.Current(); cur != nil && cur.N == n {
		fmt.Printf("revision %d is what is installed: nothing to do\n", n)
		return result(installedResult{Installed: versionOf("", rec.Version, rec.Pack), Target: s.Target, Installation: s.ID})
	} else if rec.State == saga.Installed && rev.Same(rec.Installation) {
		fmt.Printf("revision %d is the same as what is installed (%s %s): nothing to do\n", n, rec.Pack, rec.Version)
		return result(installedResult{Installed: versionOf("", rec.Version, rec.Pack), Target: s.Target, Installation: s.ID})
	}
	return rollForwardTo(ctx, s, curB, curRel, rev.Installation, fmt.Sprintf("revision %d", n))
}

// rollForwardTo runs a saga from what is installed to in — planned and run as an apply is — or, with --dry-run, says
// its plan. what names in for the messages.
func rollForwardTo(ctx context.Context, s *session.Session, curB *bundle.Bundle, curRel *render.Release, in saga.Installation, what string) error {
	toB, toRel, err := s.Version(in)
	if err != nil {
		return fmt.Errorf("%s, %s: %w", what, in.Pack, err)
	}
	name := ""
	if toB != nil {
		name = toB.Name
	}
	p, err := plan.Build(plan.Input{From: curB, To: toB, FromOut: curRel, ToOut: toRel})
	if err != nil {
		return err
	}
	plan.Seal(p, curRel, toRel)
	fmt.Print(p.Text())
	if rollbackDry {
		return result(p)
	}
	if err := approved(s, p, rollbackApprove, howRollback(rollbackTo)); err != nil {
		return err
	}
	if err := s.RollForward(ctx, p, curRel, toRel, in); err != nil {
		return withCode(CodeSagaFailed, err)
	}
	fmt.Printf("back to %s: %s %s\n", what, in.Pack, in.Version)
	return result(installedResult{Installed: versionOf(name, in.Version, in.Pack), Target: s.Target, Installation: s.ID, Plan: p})
}
