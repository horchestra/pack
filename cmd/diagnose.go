package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/diagnose"
	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/internal/storage"
	"github.com/horchestra/pack/internal/valuesfile"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/target"
)

func init() {
	diagnoseCmd.Flags().StringVar(&diagnoseApply, "apply", "", "take this way out (r1, r2 …): a value is written into values.yaml as pack answer writes one; a command is said, not run")
	rootCmd.AddCommand(diagnoseCmd)
}

var diagnoseApply string

// logLines is how many lines of each instance diagnose asks the target for.
const logLines = 50

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
		c, err := caseOf(ctx, s, rec, true)
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

// caseOf makes the case of the record's unfinished saga: the node that failed in its journal, its lines there and —
// with logs — the tail of its logs from its target, worked against the diagnoses of the pack of the version the saga
// went to. Without logs it asks the target nothing: what pack next needs, cheaply.
func caseOf(ctx context.Context, s *session.Session, rec *saga.Record, logs bool) (diagnose.Case, error) {
	sg := rec.Saga
	in := diagnose.Input{Saga: sg.ID, Error: sg.Error}
	if dir.Journal != "off" {
		entries, err := storage.FileJournal{Dir: dir.JournalPath(), Installation: s.ID}.Read(sg.ID)
		if err == nil {
			for _, e := range slices.Backward(entries) {
				if e.Kind == saga.KindFail && len(e.Node) > 0 {
					in.Key = e.Node
					if len(e.Error) > 0 {
						in.Error = e.Error
					}
					break
				}
			}

			for _, e := range entries {
				if len(in.Key) > 0 && e.Node == in.Key && (e.Kind == saga.KindLog || e.Kind == saga.KindFail) {
					in.Lines = append(in.Lines, strings.TrimSpace(e.Message+" "+e.Error))
				}
			}
		}
	}

	guessed := len(in.Key) == 0
	if guessed {
		in.Key, _, _ = strings.Cut(sg.Error, ":") // the error names the node that failed — when it is a key, below
	}
	// the diagnoses of the key's pack in the version the saga went to, their fixes worked out on its values
	if b, err := s.Pack(sg.Pack); err == nil {
		if _, ok := b.KeyPack[in.Key]; !ok && guessed {
			in.Key = "" // what the error began with is no key of the pack
		}
		if p := b.Packs[b.KeyPack[in.Key]]; p != nil {
			in.Diagnoses = p.Schema.Root.Diagnoses
		}
		var v render.Values
		if len(sg.Values) > 0 {
			_ = json.Unmarshal(sg.Values, &v)
		}
		key := in.Key
		in.Eval = func(expr string) (any, error) { return render.Eval(b, v, s.Ctx, key, expr) }
		in.Pointer = func(ptr string) string { return b.External("/" + key + ptr) }
	}
	// the tail of the key's logs, where its target gives them — after the version is rendered, for the target to know
	// the key (where it runs, its services)
	if logs && len(in.Key) > 0 {
		_, _, _ = s.Version(sg.Installation)
		tg := any(s.Inst)
		if name := s.Env.Keys[in.Key].Target; len(name) > 0 {
			tg = s.Execs[name]
		}
		if l, ok := tg.(target.Logger); ok {
			if logs, err := l.Logs(ctx, in.Key, logLines); err == nil {
				for inst, lines := range logs {
					for _, line := range lines {
						in.Lines = append(in.Lines, inst+": "+line)
					}
				}
			}
		}
	}
	in.Lang, _ = valuesLangOf(ctx) // an environment that does not read: the base language
	return diagnose.Make(in), nil
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
