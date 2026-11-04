package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/environment"
	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/render"
)

// how is the command a plan was asked for by: the one that applies it (pack apply, pack rollback --to 3), and the one
// that shows it without doing anything (pack plan, pack rollback --to 3 --dry-run).
type how struct{ run, look string }

var (
	howApply  = how{"pack apply", "pack plan"}
	howDelete = how{"pack delete", "pack delete --dry-run"}
)

// howRollback is pack rollback's, with --to n when there is one.
func howRollback(to int) how {
	run := "pack rollback"
	if to != 0 {
		run = fmt.Sprintf("pack rollback --to %d", to)
	}
	return how{run, run + " --dry-run"}
}

// next is what to run on a refusal: look at the plan, and apply it by its digest.
func (h how) next(digest string) []string {
	return []string{h.look, h.run + " --approve " + digest}
}

// approved: the plan may be applied — run.approve lets it be unasked, or --approve names its digest. A plan that does
// nothing needs no approval; a removal and the undoing of a saga do something without steps of a plan.
func approved(s *session.Session, p *plan.Plan, approve string, h how) error {
	policy, err := s.Env.Run.ApprovePolicy()
	if err != nil {
		return err
	}
	if len(p.Steps) == 0 && p.Direction != plan.DirUninstall && p.Direction != plan.DirUndo {
		if len(approve) > 0 {
			fmt.Fprintf(os.Stderr, "--approve %s: the plan does nothing, there is nothing to approve\n", approve)
		}
		return nil
	}
	if len(approve) > 0 {
		if approve != p.Digest {
			return withNext(api.CodeNeedsApproval, fmt.Errorf("--approve %s: the plan is %s now — look at it again (%s)", approve, p.Digest, h.look), h.next(p.Digest))
		}
		return nil
	}
	switch {
	case policy == environment.ApproveAuto:
		return nil
	case policy == environment.ApproveAutoSafe && len(p.Risks) == 0:
		return nil
	}
	why := "run.approve: plan — no plan applies unasked"
	var problems []render.Problem
	if policy == environment.ApproveAutoSafe {
		var kinds []string
		for _, r := range p.Risks {
			kinds = append(kinds, r.Kind)
			problems = append(problems, render.Problem{Kind: render.KindRisk, Where: r.Key, Rule: r.Kind, Message: r.Why})
		}
		why = "run.approve: auto-safe, and the plan has risks: " + strings.Join(kinds, ", ")
	}
	err = withProblems(api.CodeNeedsApproval, fmt.Errorf("%s — %s --approve %s runs this very plan", why, h.run, p.Digest), problems)
	err.(*coded).next = h.next(p.Digest)
	return err
}
