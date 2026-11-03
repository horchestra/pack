package cmd

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/horchestra/pack/sdk/check"
	"github.com/horchestra/pack/sdk/docs"
	"github.com/horchestra/pack/sdk/schema"
	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/internal/values"
	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/render"
)

func init() {
	checkCmd.Flags().BoolVar(&checkStrict, "strict", false, "what a pack's docs/ does not say is a problem, not a warning")
	rootCmd.AddCommand(checkCmd)
}

var checkStrict bool

var checkCmd = &cobra.Command{
	Use:   "check [dir|ref]",
	Short: "author checks on a component or composition directory, or a pack in a registry",
	Long: `Author checks on a component directory (schema.json, templates/, image.json), on a
composition directory (its components are the sibling dirs), or on a pack in a
registry. With no argument, the pack of release.yaml, and the release's values
rendered against it.`,
	Args: cobra.MaximumNArgs(1),
	Run: run(func(_ *cobra.Command, args []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		var problems []check.Problem
		var err error
		if len(args) > 0 {
			problems, err = checkPack(ctx, args[0], nil)
		} else {
			problems, err = checkRelease(ctx)
		}
		if err != nil {
			return err
		}
		n := 0
		for _, p := range problems {
			if checkStrict {
				p.Warning = false
			}
			fmt.Println(p)
			if !p.Warning {
				n++
			}
		}
		if n > 0 {
			return fmt.Errorf("%d problem(s)", n)
		}
		fmt.Println("ok")
		return nil
	}),
}

// checkRelease checks the release's pack, and renders the release's values against it.
func checkRelease(ctx context.Context) ([]check.Problem, error) {
	ref, err := dir.Release.PackRef()
	if err != nil {
		return nil, err
	}
	s, err := open(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return checkPack(ctx, ref, s)
}

// checkPack runs the author checks on dir (a directory or a registry reference); with a session the release's
// values are rendered against a composition too.
func checkPack(ctx context.Context, ref string, s *session.Session) ([]check.Problem, error) {
	var b *bundle.Bundle
	var err error
	if s != nil {
		b, err = s.Pack(ref) // the release's own: by its lock
	} else {
		b, err = session.OpenPack(ctx, dir, ref)
	}
	if err != nil {
		return nil, err
	}
	problems, err := render.CheckBundle(b)
	if err != nil {
		return nil, err
	}
	problems = append(problems, docsGaps(b)...)
	if s == nil {
		return problems, nil
	}
	v, err := values.Load(s.Dir.Release.ValuesFiles())
	if err != nil {
		return nil, err
	}
	if _, err := render.Render(b, v, s.Ctx); err != nil {
		problems = append(problems, check.Problem{Path: "release", Message: err.Error()})
	}
	return problems, nil
}

// docsGaps are the warnings of what the docs/ of each pack of the bundle does not say.
func docsGaps(b *bundle.Bundle) []check.Problem {
	var out []check.Problem
	for _, name := range slices.Sorted(maps.Keys(b.Packs)) {
		s, err := schema.Parse(b.Packs[name].SchemaRaw)
		if err != nil {
			continue // the checks above said it
		}
		for _, g := range docs.Gaps(s) {
			out = append(out, check.Problem{Path: name + ":" + g.Path, Message: g.Message, Warning: true})
		}
	}
	return out
}
