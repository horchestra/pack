package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/sdk/docs"
	"github.com/horchestra/pack/sdk/format"
	"github.com/horchestra/pack/sdk/schema"
)

func init() {
	docsSkeletonCmd.Flags().StringVarP(&docsLang, "lang", "l", "", "the language of the file: the base one, or a translation (required)")
	docsSkeletonCmd.Flags().StringVarP(&docsOut, "output", "o", "", "write the file here instead of printing it")
	_ = docsSkeletonCmd.MarkFlagRequired("lang")
	docsCmd.AddCommand(docsSkeletonCmd)
	rootCmd.AddCommand(docsCmd)
}

var (
	docsLang string
	docsOut  string
)

var docsCmd = &cobra.Command{
	Use:   "docs",
	Short: "the documentation of a pack's settings: docs/<lang>.yaml next to its source",
}

var docsSkeletonCmd = &cobra.Command{
	Use:   "skeleton <dir|schema.json>",
	Short: "docs/<lang>.yaml for a generated pack: every field, group and rule, \"\" where the language says nothing",
	Long: `Reads a generated pack (its directory, or its schema.json) and writes docs/<lang>.yaml
for it: every field, group and rule of the pack, with what the schema already says in
the language and "" where it says nothing. In a translation the base language's text is
a comment above each entry. The file to start a language with, or to see what a language
lacks; the comments of an existing file are not kept.`,
	Args: cobra.ExactArgs(1),
	Run: run(func(_ *cobra.Command, args []string) error {
		path := args[0]
		if st, err := os.Stat(path); err == nil && st.IsDir() {
			path = filepath.Join(path, format.SchemaFile)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s, err := schema.Parse(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if len(docs.Langs(s)) == 0 {
			_, _ = fmt.Fprintf(os.Stderr, "pack: %s has no docs/ yet: %s is taken as its base language, every field \"\" to fill\n", path, docsLang)
		}
		out := docs.Skeleton(s, docsLang)
		if len(docsOut) == 0 {
			_, err = os.Stdout.Write(out)
			return err
		}
		return os.WriteFile(docsOut, out, 0o644)
	}),
}
