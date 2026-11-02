package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/saga/storage"
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
			return nil
		}
		if *list {
			for _, r := range runs {
				fmt.Println(r)
			}
			return nil
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
		for _, e := range entries {
			if len(*node) == 0 || e.Node == *node {
				fmt.Println(e)
			}
		}
		return nil
	})
	return cmd
}
