package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/pkg/saga"
)

func init() {
	historyCmd.Flags().BoolVar(&historyJSON, "json", false, "the revisions as JSON, the values with them")
	rootCmd.AddCommand(historyCmd)
}

var historyJSON bool

var historyCmd = &cobra.Command{
	Use:   "history",
	Short: "the revisions of the installation: every saga that ended, and the removals",
	Long: `The revisions of the installation, the oldest first: every saga that ended — installed,
or undone (taken back by rollback) — and every removal, with when, the pack (by digest)
and its version. The one installed now is marked *. pack rollback --to <n> goes back to
a revision that was installed. As many are kept as run.journal_keep says of the journal.`,
	Args: cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		s, err := open(ctx, true)
		if err != nil {
			return err
		}
		defer s.Close()
		rec, err := s.Store.Load()
		if err != nil {
			return err
		}
		if rec == nil || len(rec.History) == 0 {
			return fmt.Errorf("no revision is recorded on %s", s.Target)
		}
		if historyJSON {
			type revision struct {
				saga.Revision
				Current bool `json:"current,omitempty"` // the one installed now
			}
			cur := rec.Current()
			out := make([]revision, len(rec.History))
			for i, h := range rec.History {
				out[i] = revision{Revision: h, Current: cur != nil && h.N == cur.N}
			}
			b, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(append(b, '\n'))
			return err
		}
		fmt.Print(historyText(rec))
		return nil
	}),
}

// historyText is the table of the revisions; the one installed now (Record.Current) is marked *.
func historyText(rec *saga.Record) string {
	current := -1
	if c := rec.Current(); c != nil {
		current = c.N
	}
	var b bytes.Buffer
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "\tN\tAT\tOUTCOME\tVERSION\tPACK")
	for _, h := range rec.History {
		mark := ""
		if h.N == current {
			mark = "*"
		}
		outcome := h.Outcome
		if len(h.Error) > 0 {
			outcome += " (" + h.Error + ")"
		}
		_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n", mark, h.N, h.At.Format("2006-01-02 15:04:05Z"), outcome, h.Version, h.Pack)
	}
	_ = w.Flush()
	return b.String()
}
