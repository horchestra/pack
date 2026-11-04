package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/internal/interrupt"
)

func init() {
	planCmd.Flags().StringVarP(&planOut, "output", "o", "", "write the plan as YAML here, besides printing it")
	rootCmd.AddCommand(planCmd)
}

var planOut string

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "the saga: a first installation, or the upgrade between two versions",
	Args:  cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		s, err := open(ctx, true)
		if err != nil {
			return err
		}
		defer s.Close()
		c, err := s.Prepare(false)
		if err != nil {
			return err
		}
		fmt.Print(c.Plan.Text())
		planned(c.Plan)
		if err := result(c.Plan); err != nil {
			return err
		}
		if len(planOut) == 0 {
			return nil
		}
		b, err := yaml.Marshal(c.Plan)
		if err != nil {
			return err
		}
		return os.WriteFile(planOut, b, 0o644)
	}),
}
