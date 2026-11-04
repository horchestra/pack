package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/storage"
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/saga"
)

var logCmd = newLogCmd()

func init() { rootCmd.AddCommand(logCmd) }

func newLogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "log [run]",
		Short: "the journal of a saga: its stages and the logs of its keys",
		Args:  cobra.MaximumNArgs(1),
	}
	list := cmd.Flags().Bool("list", false, "list the sagas of the installation")
	prune := cmd.Flags().Int("prune", -1, "remove all but this many newest sagas of the installation")
	node := cmd.Flags().String("node", "", "only the lines of this key")
	cmd.Run = run(func(_ *cobra.Command, args []string) error {
		installation := ""
		if rec, err := dir.FileState().Load(); err != nil {
			return err
		} else if rec != nil {
			installation = rec.ID
		}
		if len(installation) == 0 && dir.Release.State == workdir.StateTarget {
			// the record is the target's; the release says the installation's id
			id, err := dir.InstallationID()
			if err != nil {
				return err
			}
			installation = id
		}
		if len(installation) == 0 {
			return fmt.Errorf("no state in %s: nothing was applied here", dir.Path)
		}
		if dir.Journal == "off" {
			return fmt.Errorf("the journal is off (--journal)")
		}
		f := storage.FileJournal{Dir: dir.JournalPath(), Installation: installation}
		runs, err := f.Runs()
		if err != nil {
			return err
		}
		if *prune >= 0 {
			gone, err := f.Prune(*prune)
			if err != nil {
				return err
			}
			fmt.Printf("removed %d saga(s)\n", len(gone))
			return result(api.LogPruneResult{Removed: gone})
		}
		if *list {
			for _, r := range runs {
				fmt.Println(r)
			}
			return result(api.LogRunsResult{Runs: runs})
		}
		if len(runs) == 0 {
			return fmt.Errorf("no journal of %s in %s", installation, dir.JournalPath())
		}
		run := runs[len(runs)-1]
		if len(args) > 0 {
			run = args[0]
		}
		entries, err := f.Read(run)
		if err != nil {
			return err
		}
		fmt.Println("saga", run)
		var shown []saga.Entry
		for _, e := range entries {
			if len(*node) == 0 || e.Node == *node {
				fmt.Println(e)
				shown = append(shown, e)
				streamEvent(api.FromEntry(e)) // the same events as the saga's, for a UI that reconnects
			}
		}
		if streamOut {
			// each entry was an event: the result says which saga, not the entries again
			return result(api.LogSagaResult{Saga: run})
		}
		truncated := len(shown) > maxEntries
		if truncated {
			shown = shown[len(shown)-maxEntries:] // the last ones: where it ended
		}
		return result(api.LogResult{Saga: run, Entries: shown, Truncated: truncated})
	})
	return cmd
}

// maxEntries is how many entries of a saga log --json gives: the last ones, the rest said truncated.
const maxEntries = 1000
