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
	"github.com/horchestra/pack/pkg/bundle"
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

// valuesFile is the release's values.yaml as pack writes it: where, what it holds now, the values of it.
type valuesFile struct {
	path   string
	have   []byte
	values map[string]any
}

// readValuesFile reads the release's values.yaml, refusing a release whose values pack does not write: in several
// files, or a whole document (rollout, skip).
func readValuesFile() (*valuesFile, error) {
	f := &valuesFile{path: filepath.Join(dir.Release.Dir, "values.yaml")}
	switch files := dir.Release.ValuesFiles(); {
	case len(files) == 0:
	case len(files) == 1 && filepath.Clean(files[0]) == filepath.Clean(f.path):
		var err error
		if f.have, err = os.ReadFile(f.path); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("the values are in %s: pack writes a release whose values are values.yaml alone", strings.Join(files, ", "))
	}
	if err := plainValues(f.have); err != nil {
		return nil, err
	}
	if len(f.have) > 0 {
		v, err := values.Load([]string{f.path})
		if err != nil {
			return nil, err
		}
		f.values = v.Values
	}
	return f, nil
}

// valuesLangOf is the language of the comments: --lang, else the environment's.
func valuesLangOf(ctx context.Context) (string, error) {
	if len(valuesLang) > 0 {
		return valuesLang, nil
	}
	env, err := environment.Load(ctx, dir.Release.Config(), nil)
	if err != nil {
		return "", err
	}
	return env.Lang, nil
}

// render is the file pack writes for values, with the sources saved: what fill and answer and values write share.
func (f *valuesFile) render(ctx context.Context, b *bundle.Bundle, values map[string]any, src valuesfile.Sources) ([]byte, error) {
	lang, err := valuesLangOf(ctx)
	if err != nil {
		return nil, err
	}
	return valuesfile.Write(b, values, src, valuesfile.Lang(lang))
}

// save writes the file for values, unless it has comments of the operator's own (rewrite: anyway), and the sources.
func (f *valuesFile) save(ctx context.Context, b *bundle.Bundle, values map[string]any, src valuesfile.Sources, rewrite bool) (string, error) {
	want, err := f.render(ctx, b, values, src)
	if err != nil {
		return "", err
	}
	if len(f.have) > 0 && !rewrite {
		if lost := valuesfile.Foreign(f.have, want); len(lost) > 0 {
			return "", withCode(CodeForeignComments, fmt.Errorf("values.yaml: %s — they would be lost; --rewrite writes it anyway", strings.Join(lost, "; ")))
		}
	}
	msg := "values.yaml is up to date"
	if !bytes.Equal(f.have, want) {
		if err := os.WriteFile(f.path, want, 0o644); err != nil {
			return "", err
		}
		f.have, f.values = want, values
		msg = "wrote " + f.path
	}
	// the sources after the values they speak of: a values.yaml not written leaves them as they were
	if _, err := os.Stat(filepath.Join(dir.Path, valuesfile.SourcesFile)); len(src) > 0 || err == nil {
		if err := src.Save(dir.Path); err != nil {
			return "", err
		}
	}
	return msg, nil
}

// writeValues is values write: what it did, or why it did not.
func writeValues(ctx context.Context) (string, error) {
	f, err := readValuesFile()
	if err != nil {
		return "", err
	}
	b, err := session.ReleasePack(ctx, dir)
	if err != nil {
		return "", err
	}
	src, err := valuesfile.LoadSources(dir.Path)
	if err != nil {
		return "", err
	}
	if valuesCheck {
		want, err := f.render(ctx, b, f.values, src)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(f.have, want) {
			return "", errors.New("values.yaml is not what pack would write (pack values write)")
		}
		return "ok", nil
	}
	return f.save(ctx, b, f.values, src, valuesRewrite)
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
