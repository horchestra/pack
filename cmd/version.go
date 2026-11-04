package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/pkg/api"
)

func init() { rootCmd.AddCommand(versionCmd) }

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "the version of the binary, and of the documents it writes",
	Long: `The version of the pack binary and the versions of the documents it writes: a program that
reads --json or --stream checks them against what it was built for.`,
	Annotations: map[string]string{makesRelease: "true"}, // it asks nothing of a release
	Args:        cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		fmt.Println("pack", buildVersion())
		return result(api.VersionResult{Version: buildVersion(),
			Formats: map[string]int{"json": api.FormatJSON, "stream": api.FormatStream}})
	}),
}
