package cmd

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/oci"
	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/diagnose"
	"github.com/horchestra/pack/pkg/environment"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/pkg/target/remote"
)

func init() { rootCmd.AddCommand(nextCmd) }

var nextCmd = &cobra.Command{
	Use:   "next",
	Short: "where the release is and what to run next: new, unrefreshed, unfilled, invalid, failed, planned, applied",
	Long: `Says where the release is — worked out from release.yaml, the lock, the values, the record of
the installation and a plan, kept nowhere — what stops it going on, and the commands to run
next, their arguments filled in. The same for a person following it step by step and for a
model that follows it instead of planning: with --json it is {state, blocking, next}.

  new          no release here                        pack init <pack> .
  unrefreshed  the pack or a target is not brought    pack refresh
  unfilled     settings the pack requires not given   pack fill, pack answer, pack check
  invalid      values the schema or a rule refuses    edit values.yaml, pack check
  failed       a saga failed or was cut short         pack diagnose, pack apply, pack rollback
               or what is installed is not well       pack status, pack log, pack apply
  planned      what is wanted is not what is there    pack plan, pack apply
  applied      what is there is what is wanted        —`,
	Args: cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		n := whatNext(ctx)
		fmt.Println("state:", n.State)
		for _, name := range slices.Sorted(maps.Keys(n.Facts)) {
			fmt.Printf("facts of %s: told %s ago\n", name, time.Since(n.Facts[name]).Round(time.Minute))
		}
		for _, b := range n.Blocking {
			fmt.Printf("  %s: %s\n", b.Code, strings.ReplaceAll(b.Message, "\n", "\n    "))
		}
		if len(n.Next) > 0 {
			fmt.Println("next:")
			for _, c := range n.Next {
				fmt.Println("  " + c)
			}
		}
		return result(n)
	}),
}

// whatNext works the state out, from the first thing a release needs to the last, and says how old the facts are.
func whatNext(ctx context.Context) api.NextState {
	n := stateNow(ctx)
	if facts, _, err := session.LoadFacts(dir); err == nil {
		for name, f := range facts {
			if n.Facts == nil {
				n.Facts = map[string]time.Time{}
			}
			n.Facts[name] = f.At
		}
	}
	return n
}

func stateNow(ctx context.Context) api.NextState {
	rel := dir.Release
	if _, err := os.Stat(filepath.Join(rel.Dir, release.File)); os.IsNotExist(err) || err == nil && len(rel.Pack) == 0 {
		return api.NextState{State: api.StateNew, Next: []string{"pack init <oci://registry/repo:tag|dir> ."}}
	} else if err != nil {
		return stopped(err)
	}
	b, err := unbrought(ctx)
	if err != nil {
		return stopped(err) // a lock or an environment that does not read: refresh would not mend it
	}
	if len(b) > 0 {
		return api.NextState{State: api.StateUnrefreshed, Blocking: b, Next: []string{"pack refresh"}}
	}
	s, err := open(ctx, true)
	if err != nil {
		return stopped(err)
	}
	defer s.Close()
	rec, err := s.Store.Load()
	if err != nil {
		return stopped(err)
	}
	if rec.Unfinished() {
		msg := fmt.Sprintf("the saga towards %s %s is %s", rec.Saga.Pack, rec.Saga.Version, rec.State)
		if len(rec.Saga.Error) > 0 {
			msg += ": " + rec.Saga.Error
		}
		next := []string{"pack diagnose", "pack log", "pack apply", "pack rollback"}
		lang, _ := valuesLangOf(ctx)
		if c, err := diagnose.CaseOf(ctx, dir, s, rec, false, lang); err == nil { // the journal's alone: next asks no target for logs
			msg += " — " + c.Class + ": " + c.Cause
			if c.Class == diagnose.Unknown || len(c.Remedies) == 0 {
				next = append(next, "a person should look: pack diagnose cannot tell what this is")
			} else if len(c.Remedies) > 0 {
				next = append([]string{"pack diagnose", "pack diagnose --apply " + c.Remedies[0].ID}, next[1:]...)
			}
		}
		return api.NextState{State: api.StateFailed, Blocking: []api.Error{{Code: api.CodeSagaUnfinished, Message: msg}}, Next: next}
	}
	c, err := s.Prepare(false)
	if err != nil {
		if ps, ok := errors.AsType[*render.Problems](err); ok {
			e := classify(err)
			if ps.OnlyRequired() {
				var where []string
				for _, p := range ps.Problems {
					where = append(where, p.Where)
				}
				return api.NextState{State: api.StateUnfilled, Blocking: []api.Error{e},
					Next: []string{"pack fill", "pack answer <pointer> <value>: " + strings.Join(where, ", "), "pack check"}}
			}
			return api.NextState{State: api.StateInvalid, Blocking: []api.Error{e}, Next: []string{"edit values.yaml", "pack check"}}
		}
		return stopped(err)
	}
	if len(c.Plan.Steps) > 0 {
		if err := approved(s, c.Plan, "", howApply); err != nil {
			return api.NextState{State: api.StatePlanned, Blocking: []api.Error{classify(err)}, Next: nextOf(err, api.CodeNeedsApproval)}
		}
		return api.NextState{State: api.StatePlanned, Next: []string{"pack plan", "pack apply"}}
	}
	// what is wanted is what the record says is there: is it there, and well
	if st, err := statusOf(ctx, s); err != nil {
		return stopped(err)
	} else if !st.Healthy && len(unwell(st)) > 0 {
		e := api.Error{Code: api.CodeUnhealthy, Message: strings.Join(unwell(st), "\n")}
		return api.NextState{State: api.StateFailed, Blocking: []api.Error{e}, Next: nextFor(api.CodeUnhealthy)}
	}
	return api.NextState{State: api.StateApplied, Next: []string{}}
}

// stopped is the state of an error that stops the release: its code says what to run.
func stopped(err error) api.NextState {
	e := classify(err)
	state := api.StateError
	if e.Code == api.CodeNotRefreshed {
		state = api.StateUnrefreshed
	}
	next := nextOf(err, e.Code)
	if next == nil {
		next = []string{}
	}
	return api.NextState{State: state, Blocking: []api.Error{e}, Next: next}
}

// unbrought is what pack refresh has yet to bring: the pack of a registry the lock does not pin, the program or the
// image of a target that is a program of its own.
func unbrought(ctx context.Context) ([]api.Error, error) {
	rel := dir.Release
	var out []api.Error
	add := func(format string, a ...any) {
		out = append(out, api.Error{Code: api.CodeNotRefreshed, Message: fmt.Sprintf(format, a...)})
	}
	lock, err := release.ReadLock(rel.Dir)
	if err != nil {
		return nil, err
	}
	if lock == nil {
		lock = &release.Lock{}
	}
	if oci.IsRef(rel.Pack) && (lock.Pack != rel.Pack || len(lock.Digest) == 0) {
		add("%s is not locked", rel.Pack)
	}
	env, err := environment.Load(ctx, rel.Config(), nil)
	if err != nil {
		return nil, err
	}
	check := func(name string, settings map[string]string) {
		if len(name) == 0 || slices.Contains(target.Names(), name) {
			return
		}
		if remote.FromImage(settings[remote.RuntimePrefix+"type"]) {
			if tl := lock.Targets[name]; len(tl.Digest) == 0 {
				add("target %s: its image is not locked", name)
			}
			return
		}
		if _, err := remote.Find(name); err != nil {
			add("target %s: its program is not in .pack/bin", name)
		}
	}
	check(env.Target, env.Settings)
	for _, t := range env.Targets {
		check(t.Target, t.Settings)
	}
	return out, nil
}
