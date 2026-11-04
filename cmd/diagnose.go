package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/pkg/diagnose"
	"github.com/horchestra/pack/pkg/valuesfile"
)

func init() {
	diagnoseCmd.Flags().StringVar(&diagnoseApply, "apply", "", "take this way out (r1, r2 …): a value is written into values.yaml as pack answer writes one; a command is said, not run")
	rootCmd.AddCommand(diagnoseCmd)
}

var diagnoseApply string

var diagnoseCmd = &cobra.Command{
	Use:   "diagnose",
	Short: "what failed and why, from the saga's journal and the key's logs, and the ways out",
	Long: `Makes a case of the saga that failed, or was cut short: the key that failed and its error, the
lines that show why — the key's of the journal, the tail of its logs where the target gives
them — what kind of failure it is, and the ways out. The pack's own diagnoses come first
(PackDiagnose: a value for a setting, worked out with the facts, or a command), then the
kinds every pack shares: port_conflict, image_pull, oom, disk, permission, timeout. A case
pack cannot tell is unknown, with no way out: a person should look.

--apply r1 takes a way out: a value goes into values.yaml as pack answer would write it (its
source diagnose), checked by the schema and the rules; a command is said, for you to run.`,
	Args: cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		s, err := open(ctx, true)
		if err != nil {
			return err
		}
		closed := false
		defer func() {
			if !closed {
				s.Close()
			}
		}()
		rec, err := s.Store.Load()
		if err != nil {
			return err
		}
		if !rec.Unfinished() {
			return errors.New("nothing failed: the last saga ended (pack log, pack history)")
		}
		lang, _ := valuesLangOf(ctx) // an environment that does not read: the base language
		c, err := diagnose.CaseOf(ctx, dir, s, rec, true, lang)
		if err != nil {
			return err
		}
		printCase(c)
		if len(diagnoseApply) == 0 {
			return result(c)
		}
		i := -1
		for j, r := range c.Remedies {
			if r.ID == diagnoseApply {
				i = j
			}
		}
		if i < 0 {
			return fmt.Errorf("no way out %s in the case", diagnoseApply)
		}
		r := c.Remedies[i]
		if len(r.Patch) == 0 {
			if len(r.Command) > 0 {
				fmt.Println("run:", r.Command)
			}
			return result(c)
		}
		s.Close() // the answer opens the release again, as pack answer does
		closed = true
		rs, err := openAsk(ctx)
		if err != nil {
			return err
		}
		defer rs.s.Close()
		if _, err := rs.apply(ctx, []answer{{Pointer: r.Patch, Value: r.Value, Source: valuesfile.FromDiagnose, Why: c.Cause}}); err != nil {
			return err
		}
		fmt.Println("next: pack plan, pack apply")
		return result(c)
	}),
}

func printCase(c diagnose.Case) {
	fmt.Printf("%s: %s (%s)\n", orNone(c.Key), c.Cause, c.Class)
	fmt.Println("  error:", c.Error)
	if len(c.Evidence) > 0 {
		fmt.Println("  evidence:")
		for _, l := range c.Evidence {
			fmt.Println("    " + l)
		}
	}
	for _, r := range c.Remedies {
		switch {
		case len(r.Patch) > 0:
			fmt.Printf("  %s: set %s to %s%s\n", r.ID, r.Patch, jsonText(r.Value), whyOf(r.Why))
		case len(r.Command) > 0:
			fmt.Printf("  %s: run %s%s\n", r.ID, r.Command, whyOf(r.Why))
		default:
			fmt.Printf("  %s: %s\n", r.ID, r.Why)
		}
	}
	switch {
	case c.Class == diagnose.Unknown:
		fmt.Println("  pack cannot tell what this is: a person should look (pack log)")
	case len(c.Remedies) == 0:
		fmt.Println("  no way out pack can offer: a person should look (pack log)")
	}
}

func orNone(s string) string {
	if len(s) == 0 {
		return "the saga"
	}
	return s
}
