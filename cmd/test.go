package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/pkg/scenario"
)

func init() {
	testCmd.Flags().StringVar(&testRelease, "release", "", "run on the target of this release — its pack, targets, environment and lock (pack refresh) — instead of the memory target: the pack is the release's, the arguments are scenarios")
	testCmd.Flags().BoolVar(&testUpdate, "update", os.Getenv("UPDATE_GOLDEN") == "1", "write the golden files (files, plan) from what the scenarios give, instead of comparing (UPDATE_GOLDEN=1)")
	rootCmd.AddCommand(testCmd)
}

var (
	testUpdate  bool
	testRelease string
)

// CodeTestFailed: a scenario of pack test did not give what it expects.
const CodeTestFailed = "test_failed"

var testCmd = &cobra.Command{
	Use:   "test <pack-dir> [scenario.yaml|dir]… | test --release <dir> [scenario.yaml|dir]…",
	Short: "run the pack's scenarios — facts, values, an environment, what they should give — with no infrastructure",
	Long: `Runs the scenarios of a pack (tests/*.yaml of the current directory, or the files and directories
named), each in a release of its own on the memory target, in this process: nothing is
installed anywhere. A scenario gives the facts of the targets (a fixture of pack discover
--save-fixture, or inline), the values, the environment, a version installed first (from), a
failure to inject (fail_at: <key>/<kind>, <key>/hook:<name>), the logs the target gives; and
says what it expects:

  suggest    the values pack fill offers, by pointer (null: none)
  problems   what the values break: where, kind, rule ([]: nothing)
  files      the rendered files, a golden directory (<key>/<path>, as pack template -o)
  plan       the plan from the version installed first, a golden file of its text
  apply      how the apply ends: state, the keys in effect
  diagnose   what pack diagnose makes of the failure: class, diagnosis, remedies

Paths are the scenario file's. --update (UPDATE_GOLDEN=1) writes the golden files.

--release <dir> runs them on the target of a release instead — its pack, targets, env.yaml
(the scenario's env over it) and lock, refreshed — and installs for real: what a scenario
installed is taken away after it. files and plan are the memory target's (its addresses) and
are not compared there; a scenario of fail_at or logs is the memory target's, and skipped.`,
	Annotations: map[string]string{makesRelease: "true"},
	Args:        cobra.ArbitraryArgs,
	Run: run(func(_ *cobra.Command, args []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		var on *scenario.Stand
		var err error
		if len(testRelease) > 0 {
			on, err = scenario.ReleaseStand(testRelease)
		} else if len(args) == 0 {
			return errors.New("the pack's directory, or --release of one")
		} else {
			on, err = scenario.MemoryStand(args[0])
			args = args[1:]
		}
		if err != nil {
			return err
		}
		files, err := scenario.Files(args)
		if err != nil {
			return err
		}
		var results []scenario.Result
		failed := 0
		for _, f := range files {
			r := scenario.RunScenario(ctx, on, f, testUpdate)
			results = append(results, r)
			if r.OK {
				fmt.Printf("ok    %s\n", r.Name)
				for _, m := range r.Skipped {
					fmt.Println("      skipped: " + m)
				}
				continue
			}
			failed++
			fmt.Printf("FAIL  %s\n", r.Name)
			for _, m := range r.Failures {
				fmt.Println("      " + strings.ReplaceAll(m, "\n", "\n      "))
			}
		}
		doc := struct {
			Scenarios []scenario.Result `json:"scenarios"`
		}{results}
		if failed > 0 {
			// one document, the error's: the scenarios are its detail
			return withDetail(CodeTestFailed, fmt.Errorf("%d of %d scenario(s) failed", failed, len(results)), doc)
		}
		fmt.Printf("%d scenario(s) ok\n", len(results))
		return result(doc)
	}),
}
