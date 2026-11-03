package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/internal/values"
	"github.com/horchestra/pack/internal/valuesfile"
	"github.com/horchestra/pack/pkg/environment"
)

func init() {
	valuesWriteCmd.Flags().BoolVar(&valuesRewrite, "rewrite", false, "write the file even when it has comments of its own, which are lost")
	valuesWriteCmd.Flags().StringVar(&valuesLang, "lang", "", "the language of the comments (env.yaml lang:, else $LANG, else the pack's base)")
	valuesWriteCmd.Flags().BoolVar(&valuesCheck, "check", false, "write nothing: say whether the file is what pack would write")
	valuesCmd.AddCommand(valuesWriteCmd)
	rootCmd.AddCommand(valuesCmd)
}

var (
	valuesRewrite bool
	valuesLang    string
	valuesCheck   bool
)

var valuesCmd = &cobra.Command{
	Use:   "values",
	Short: "the release's values.yaml",
}

var valuesWriteCmd = &cobra.Command{
	Use:   "write",
	Short: "write values.yaml: every setting of the pack, documented, the release's values kept",
	Long: `Writes the release's values.yaml from the pack's schema: every setting of the pack and of
the packs it holds, in the schema's order, its documentation (docs/, in the release's
language) as comments above it. A value the release gives is kept as it is; a setting it
does not give is a commented line — its default, or an example — to uncomment.

A file whose comments are not the ones pack wrote (a note of the operator's) is not
rewritten: --rewrite writes it anyway, and those comments are lost. The values stay in
one file, values.yaml: a release whose values are split over files or that is a whole
document (rollout, skip) is written by hand.`,
	Args: cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		msg, err := writeValues(ctx)
		if err != nil {
			return err
		}
		fmt.Println(msg)
		return result(struct {
			Result string `json:"result"`
		}{msg})
	}),
}

// writeValues is values write: what it did, or why it did not.
func writeValues(ctx context.Context) (string, error) {
	path := filepath.Join(dir.Release.Dir, "values.yaml")
	var have []byte
	switch files := dir.Release.ValuesFiles(); {
	case len(files) == 0:
	case len(files) == 1 && filepath.Clean(files[0]) == filepath.Clean(path):
		var err error
		if have, err = os.ReadFile(path); err != nil && !os.IsNotExist(err) {
			return "", err
		}
	default:
		return "", fmt.Errorf("the values are in %s: pack writes a release whose values are values.yaml alone", strings.Join(files, ", "))
	}
	if err := plainValues(have); err != nil {
		return "", err
	}
	var given map[string]any
	if len(have) > 0 {
		v, err := values.Load([]string{path})
		if err != nil {
			return "", err
		}
		given = v.Values
	}
	b, err := session.ReleasePack(ctx, dir)
	if err != nil {
		return "", err
	}
	lang := valuesLang
	if len(lang) == 0 {
		env, err := environment.Load(ctx, dir.Release.Config(), nil)
		if err != nil {
			return "", err
		}
		lang = env.Lang
	}
	src, err := valuesfile.LoadSources(dir.Path)
	if err != nil {
		return "", err
	}
	want, err := valuesfile.Write(b, given, src, valuesfile.Lang(lang))
	if err != nil {
		return "", err
	}
	if valuesCheck {
		if !bytes.Equal(have, want) {
			return "", errors.New("values.yaml is not what pack would write (pack values write)")
		}
		return "ok", nil
	}
	if len(have) > 0 && !valuesRewrite {
		if lost := valuesfile.Foreign(have, want); len(lost) > 0 {
			return "", withCode(CodeForeignComments, fmt.Errorf("values.yaml: %s — they would be lost; --rewrite writes it anyway", strings.Join(lost, "; ")))
		}
	}
	if bytes.Equal(have, want) {
		return "values.yaml is up to date", nil
	}
	if err := os.WriteFile(path, want, 0o644); err != nil {
		return "", err
	}
	return "wrote " + path, nil
}

// plainValues refuses a values file that is a whole document (values:, rollout:, skip:): what is not values would be
// lost.
func plainValues(raw []byte) error {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("values.yaml: %w", err)
	}
	if len(doc) == 0 {
		return nil
	}
	for k := range doc {
		if k != "values" && k != "rollout" && k != "skip" {
			return nil
		}
	}
	return errors.New("values.yaml is a whole document (values, rollout, skip): pack writes a file of values alone")
}
