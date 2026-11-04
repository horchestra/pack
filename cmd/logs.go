package cmd

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/pkg/target"
)

func init() {
	logsCmd.Flags().StringVar(&logsKey, "key", "", "only this key of the installation")
	logsCmd.Flags().StringVar(&logsInstance, "instance", "", "only this instance of the key")
	logsCmd.Flags().IntVar(&logsTail, "tail", 50, "the last lines of each instance (0: all the target gives)")
	rootCmd.AddCommand(logsCmd)
}

var (
	logsKey      string
	logsInstance string
	logsTail     int
)

// logsDoc is what pack logs --json says: the lines of each key by instance, and the keys whose target gives none.
type logsDoc struct {
	Logs   map[string]map[string][]string `json:"logs"`
	NoLogs []string                       `json:"no_logs,omitempty"`
}

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "the logs of the installed keys, by instance, from their targets",
	Long: `Asks the target of each installed key — one with --key — for the tail of what its instances
wrote: the same logs pack diagnose reads. Only reading: nothing changes. A target that gives
no logs is said so; --instance names one instance of the key.`,
	Args: cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		s, err := open(ctx, true)
		if err != nil {
			return err
		}
		defer s.Close()
		rec, _, rel, err := s.Installed()
		if err != nil {
			return err
		}
		if rec == nil || rel == nil {
			return errors.New("nothing installed here yet (pack apply)")
		}
		var keys []string
		for _, u := range rel.Units {
			keys = append(keys, u.Key)
		}
		if len(logsKey) > 0 {
			if !slices.Contains(keys, logsKey) {
				return fmt.Errorf("no key %q installed: the keys are %s", logsKey, strings.Join(keys, ", "))
			}
			keys = []string{logsKey}
		}
		out := logsDoc{Logs: map[string]map[string][]string{}}
		for _, key := range keys {
			tg := any(s.Inst)
			if name := s.Env.Keys[key].Target; len(name) > 0 {
				tg = s.Execs[name]
			}
			l, ok := tg.(target.Logger)
			if !ok {
				out.NoLogs = append(out.NoLogs, key)
				continue
			}
			logs, err := l.Logs(ctx, key, logsTail)
			if errors.Is(err, target.ErrNoLogs) {
				out.NoLogs = append(out.NoLogs, key)
				continue
			}
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			if len(logsInstance) > 0 {
				lines, ok := logs[logsInstance]
				if !ok {
					return fmt.Errorf("no instance %q of %s: the instances are %s", logsInstance, key, strings.Join(slices.Sorted(maps.Keys(logs)), ", "))
				}
				logs = map[string][]string{logsInstance: lines}
			}
			out.Logs[key] = logs
		}
		for _, key := range keys {
			for _, inst := range slices.Sorted(maps.Keys(out.Logs[key])) {
				fmt.Printf("%s/%s:\n", key, inst)
				for _, line := range out.Logs[key][inst] {
					fmt.Println("  " + line)
				}
			}
		}
		for _, key := range out.NoLogs {
			fmt.Printf("%s: the target gives no logs\n", key)
		}
		return result(out)
	}),
}
