package cmd

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/saga"
)

func init() {
	runCmd.Flags().StringVar(&runKey, "key", "", "the key to run the action on, when several keys have an action of the name")
	runCmd.Flags().StringArrayVar(&runParams, "param", nil, "a parameter of the action, name=value; again for another")
	runCmd.Flags().BoolVar(&runDry, "dry-run", false, "say what would be run, its digest, run nothing")
	runCmd.Flags().StringVar(&runApprove, "approve", "", "run the action of this digest (--dry-run says it), whatever run.approve says")
	rootCmd.AddCommand(runCmd)
}

var (
	runKey     string
	runParams  []string
	runDry     bool
	runApprove string
)

var runCmd = &cobra.Command{
	Use:   "run [action]",
	Short: "run an action of the pack on the installation — backup, restore … — or list them",
	Long: `Runs one of the actions the pack names (PackActions: backup, restore, rotate …) on a key of
what is installed: in the key's first instance as it runs (docker exec, the cluster's exec,
the key's first host), with its files, data, network, environment and secrets, on the key's
target; its lines go to the journal as a run of its own (pack log). A file it writes outlives
it on a volume of the key. Without an action, lists them with their parameters. The actions are a closed list:
what may be run besides apply is what the pack says.

An action that changes the installation (mutating) is a plan to run.approve: asked for under
plan; one the pack says is never run unasked (approve) is a risk, asked for under auto-safe
too. --approve <digest> runs the one --dry-run showed.`,
	Args: cobra.MaximumNArgs(1),
	Run: run(func(_ *cobra.Command, args []string) error {
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
			return withCode(api.CodeNothingInstalled, fmt.Errorf("nothing is installed on %s: an action runs on what is", s.Target))
		}
		actions := session.Actions(b, rel)
		if len(args) == 0 {
			printActions(actions)
			out := api.ActionsResult{Actions: make([]api.Action, len(actions))}
			for i, a := range actions {
				out.Actions[i] = api.Action(a)
			}
			return result(out)
		}
		if rec.State != saga.Installed {
			return withCode(api.CodeSagaUnfinished, fmt.Errorf("the installation is %s: an action runs on what a saga finished (pack next)", rec.State))
		}
		a, err := session.FindAction(actions, args[0], runKey)
		if err != nil {
			return err
		}
		params, err := a.Params(runParams)
		if err != nil {
			return withCode(api.CodeValuesInvalid, err)
		}
		p, err := a.Plan(b, rel, params)
		if err != nil {
			return err
		}
		st := p.Steps[0]
		fmt.Printf("%s on %s (%s %s): %s\n", st.Name, st.Key, p.Pack, p.From, strings.Join(st.Command, " "))
		for _, r := range p.Risks {
			fmt.Printf("risk: %s: %s\n", r.Kind, r.Why)
		}
		fmt.Println("plan", p.Digest)
		ran := api.RanResult{Action: a.Action.Name, Key: a.Key, Params: params, Digest: p.Digest, DryRun: runDry}
		if runDry {
			return result(ran)
		}
		if a.Action.Mutating || a.Action.Approve {
			if err := approved(s, p, runApprove, howRun(a, runParams)); err != nil {
				return err
			}
		}
		if err := s.RunAction(ctx, rel, p); err != nil {
			return withCode(api.CodeSagaFailed, err)
		}
		fmt.Printf("%s on %s: done\n", a.Action.Name, a.Key)
		return result(ran)
	}),
}

// howRun is pack run's for the action, with its key and parameters as given.
func howRun(a session.Action, params []string) how {
	words := []string{"pack run", a.Action.Name}
	if len(runKey) > 0 {
		words = append(words, "--key", runKey)
	}
	for _, p := range params {
		words = append(words, "--param", p)
	}
	run := strings.Join(words, " ")
	return how{run, run + " --dry-run"}
}

func printActions(actions []session.Action) {
	if len(actions) == 0 {
		fmt.Println("the installed version has no actions")
		return
	}
	for _, a := range actions {
		var marks []string
		if a.Action.Mutating {
			marks = append(marks, "mutating")
		}
		if a.Action.Approve {
			marks = append(marks, "asks for approval")
		}
		line := a.Action.Name + " (" + a.Key + ")"
		if len(marks) > 0 {
			line += " [" + strings.Join(marks, ", ") + "]"
		}
		if len(a.Action.Description) > 0 {
			line += ": " + a.Action.Description
		}
		fmt.Println(line)
		if a.Action.Schema == nil {
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(a.Action.Schema.Properties)) {
			p := a.Action.Schema.Properties[name]
			word := p.Type
			if slices.Contains(a.Action.Schema.Required, name) {
				word += ", required"
			}
			if p.Default != nil {
				word += fmt.Sprintf(", default %v", p.Default)
			}
			if len(p.Enum) > 0 {
				word += fmt.Sprintf(", one of %v", p.Enum)
			}
			fmt.Printf("  --param %s=… (%s) %s\n", name, word, p.Description)
		}
	}
}
