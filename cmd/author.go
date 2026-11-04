package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	packdocs "github.com/horchestra/pack/docs"
	packexamples "github.com/horchestra/pack/examples"
	"github.com/horchestra/pack/internal/author"
	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/internal/target/memory"
	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/environment"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/scenario"
	"github.com/horchestra/pack/sdk/docs"
	"github.com/horchestra/pack/sdk/format"
	"github.com/horchestra/pack/sdk/schema"
)

func init() {
	for _, c := range []*cobra.Command{authorCheckCmd, authorDocsCmd} {
		c.Flags().StringVar(&authorLang, "lang", "", "the language of the source: go, python or java (by default what its files are)")
		c.Flags().StringVar(&authorClasspath, "classpath", "", "Java: the SDK's classes and what else the pack needs (CLASSPATH by default)")
		c.Flags().StringArrayVar(&authorSources, "source", nil, "Java: another source file compiled with the pack's — a pack it holds, written elsewhere; again for another")
	}
	authorCheckCmd.Flags().StringVar(&authorTests, "tests", "", "the scenarios to run (pack test): by default tests/ beside the source or above it")
	authorDocsCmd.Flags().StringVarP(&docsLang, "lang-docs", "l", "", "the language of the file to write: the base one, or a translation (required)")
	_ = authorDocsCmd.MarkFlagRequired("lang-docs")
	authorCelCmd.Flags().StringVar(&authorPack, "pack", "", "the generated pack (its directory) whose key the expression is of (required)")
	authorCelCmd.Flags().StringVar(&authorKey, "key", "", "the key the expression is of: what values and facts are (by default the pack's only key)")
	authorCelCmd.Flags().StringVar(&authorValues, "values", "", "the values, a YAML file as values.yaml is")
	authorCelCmd.Flags().StringVar(&authorFacts, "facts", "", "the facts of the targets, a fixture of pack discover --save-fixture")
	authorCelCmd.Flags().StringVar(&authorEnv, "env", "", "an env.yaml whose keys give the limits a formula reads")
	_ = authorCelCmd.MarkFlagRequired("pack")
	authorRefCmd.Flags().StringVar(&authorShowLang, "lang", author.Go, "the language of the examples: go, python or java")
	authorExampleCmd.Flags().StringVar(&authorShowLang, "lang", author.Go, "the language of the example: go, python or java")
	authorScaffoldCmd.Flags().StringVar(&scaffoldSpec, "spec", "", "the spec of the pack, YAML or JSON (--schema says its schema)")
	authorScaffoldCmd.Flags().StringVar(&scaffoldFrom, "from", "", "a generated pack (its directory) to write the source of again: the pack ported to another language")
	authorScaffoldCmd.Flags().StringVar(&scaffoldLang, "lang", author.Go, "the language to write: go, python or java")
	authorScaffoldCmd.Flags().StringVarP(&scaffoldOut, "output", "o", ".", "the directory to write the source into")
	authorScaffoldCmd.Flags().StringVar(&scaffoldGoImport, "go-import", "", "Go: the import path of the package written (by default the go.mod above the directory says it)")
	authorScaffoldCmd.Flags().BoolVar(&scaffoldForce, "force", false, "write over the files of a scaffold already in the directory (by default they are refused, none written)")
	authorScaffoldCmd.Flags().BoolVar(&scaffoldSchema, "schema", false, "say the JSON Schema of a spec — what a model's output is constrained by — and write nothing")
	authorCmd.AddCommand(authorCheckCmd, authorCelCmd, authorRefCmd, authorExampleCmd, authorDocsCmd, authorScaffoldCmd)
	rootCmd.AddCommand(authorCmd)
}

var (
	authorLang, authorClasspath, authorTests         string
	authorSources                                    []string
	authorPack, authorKey, authorValues, authorFacts string
	authorEnv                                        string
	authorShowLang                                   string // ref's and example's: the language shown

	scaffoldSpec, scaffoldFrom, scaffoldLang, scaffoldOut, scaffoldGoImport string
	scaffoldSchema, scaffoldForce                                           bool
)

var authorCmd = &cobra.Command{
	Use:   "author",
	Short: "the author's tools: generate and check a pack's source in one go, work a CEL expression out, the vocabulary, the examples",
	Long: `What the author of a pack — a person, or a model writing the code from a prompt — runs while
writing it: check (generate, pack check, docs, the scenarios, in one command, with where in
the source each problem is), cel (an expression worked out on values and facts), ref (the
part of authoring.md a topic is), example (an example pack in a language), docs (the
docs/<lang>.yaml to fill). The same are tools of pack mcp.`,
	Annotations: map[string]string{makesRelease: "true"},
}

// authorProblem is a problem of the pack, with where in the source it is when that is found.
type authorProblem struct {
	Pack    string `json:"pack"`
	Path    string `json:"path"`
	Message string `json:"message"`
	Warning bool   `json:"warning,omitempty"`
	Source  string `json:"source,omitempty"` // file:line of the source
}

// authorChecked is what author check says.
type authorChecked struct {
	OK        bool              `json:"ok"`
	Lang      string            `json:"lang"`
	Generate  string            `json:"generate,omitempty"` // what the generator said when it failed: a compiler's file:line, the SDK's refusal
	Problems  []authorProblem   `json:"problems,omitempty"`
	Scenarios []scenario.Result `json:"scenarios,omitempty"`
}

var authorCheckCmd = &cobra.Command{
	Use:   "check [source-dir]",
	Short: "generate the pack from its source, check it, its docs and its scenarios — what to fix, with where",
	Long: `Generates the pack from its source in the directory (the current one by default) with the
SDK of its language — go run of the package of sdk.Run (./cmd when there is one), python3 of
the file of packsdk.main, javac and java of the class of PackSdk.main — then runs on what it
wrote pack check and the docs checks, and the scenarios of tests/ beside the source or above
it (pack test). Each problem says the field's pointer and, when it is found, the file:line of
the source that writes the field. ok when nothing but warnings is said: run it again after
each fix until it is.`,
	Args: cobra.MaximumNArgs(1),
	Run: run(func(_ *cobra.Command, args []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		src := "."
		if len(args) > 0 {
			src = args[0]
		}
		src, err := filepath.Abs(src)
		if err != nil {
			return err
		}
		out, root, lang, res, err := generateFrom(ctx, src)
		if len(out) > 0 {
			defer os.RemoveAll(out)
		}
		if err != nil {
			return err
		}
		if len(res.Generate) == 0 {
			names, _ := os.ReadDir(out)
			for _, e := range names {
				if !e.IsDir() {
					continue
				}
				ps, err := checkPack(ctx, filepath.Join(out, e.Name()), nil)
				if err != nil {
					res.Problems = append(res.Problems, authorProblem{Pack: e.Name(), Path: "/", Message: err.Error()})
					continue
				}
				for _, p := range ps {
					ap := authorProblem{Pack: e.Name(), Path: p.Path, Message: p.Message, Warning: p.Warning}
					if w, ok := author.Locate(src, lang, p.Path); ok {
						ap.Source = relTo(src, w)
					}
					res.Problems = append(res.Problems, ap)
				}
			}
			if tests := testsOf(src, authorTests); len(tests) > 0 && len(root) > 0 {
				// tests/ found beside the source has scenarios (testsOf saw them); one named that has none is an error,
				// never an ok of nothing run
				files, err := scenario.Files([]string{tests})
				if err != nil {
					return fmt.Errorf("--tests: %w", err)
				}
				on, err := scenario.MemoryStand(filepath.Join(out, root))
				if err != nil {
					return err
				}
				for _, f := range files {
					res.Scenarios = append(res.Scenarios, scenario.RunScenario(ctx, on, f, false))
				}
			}
		}
		res.OK = len(res.Generate) == 0 && !slices.ContainsFunc(res.Problems, func(p authorProblem) bool { return !p.Warning }) &&
			!slices.ContainsFunc(res.Scenarios, func(r scenario.Result) bool { return !r.OK })
		printChecked(res)
		if !res.OK {
			// one document, the error's: what to fix is its detail
			return withDetail(api.CodeCheckFailed, errors.New("the pack's source has problems to fix"), res)
		}
		return result(res)
	}),
}

// generateFrom runs the generator of the source in src into a directory of its own: the directory, the name of the
// pack that holds the others, the language, and — when the generator failed — what it said.
func generateFrom(ctx context.Context, src string) (out, root, lang string, res authorChecked, err error) {
	lang = authorLang
	if len(lang) == 0 {
		if lang, err = author.Detect(src); err != nil {
			return "", "", "", res, err
		}
	}
	res.Lang = lang
	if out, err = os.MkdirTemp("", "pack-author-"); err != nil {
		return "", "", "", res, err
	}
	err = author.Generate(ctx, src, lang, out, author.Options{Classpath: authorClasspath, Sources: authorSources})
	if ge, ok := errors.AsType[*author.GenerateError](err); ok {
		res.Generate = ge.Said
		return out, "", lang, res, nil
	}
	if err != nil {
		return out, "", lang, res, err
	}
	root, err = rootPack(out)
	return out, root, lang, res, err
}

// rootPack is the pack of the generated directory that no other holds.
func rootPack(out string) (string, error) {
	ents, err := os.ReadDir(out)
	if err != nil {
		return "", err
	}
	held := map[string]bool{}
	var names []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		names = append(names, e.Name())
		b, err := bundle.Load(filepath.Join(out, e.Name()))
		if err != nil {
			continue
		}
		for name := range b.Packs {
			if name != b.Name {
				held[name] = true
			}
		}
	}
	for _, n := range names {
		if !held[n] {
			return n, nil
		}
	}
	return "", fmt.Errorf("the generator wrote no pack under %s", out)
}

// testsOf is the directory of scenarios of a source: the one named, else tests/ beside it, else above it.
func testsOf(src, named string) string {
	if len(named) > 0 {
		return named
	}
	for _, d := range []string{filepath.Join(src, "tests"), filepath.Join(filepath.Dir(src), "tests")} {
		if m, _ := filepath.Glob(filepath.Join(d, "*.yaml")); len(m) > 0 {
			return d
		}
	}
	return ""
}

func relTo(src string, w author.Where) string {
	if r, err := filepath.Rel(src, w.File); err == nil {
		w.File = r
	}
	return w.String()
}

func printChecked(r authorChecked) {
	if len(r.Generate) > 0 {
		fmt.Println("the generator failed:")
		fmt.Println("  " + strings.ReplaceAll(r.Generate, "\n", "\n  "))
	}
	for _, p := range r.Problems {
		kind := "problem"
		if p.Warning {
			kind = "warning"
		}
		where := p.Pack + ":" + p.Path
		if len(p.Source) > 0 {
			where += " (" + p.Source + ")"
		}
		fmt.Printf("%s: %s: %s\n", kind, where, p.Message)
	}
	for _, s := range r.Scenarios {
		if !s.OK {
			fmt.Printf("scenario failed: %s\n", s.Name)
			for _, f := range s.Failures {
				fmt.Println("  " + strings.ReplaceAll(f, "\n", "\n  "))
			}
		}
	}
	if r.OK {
		fmt.Println("ok")
	}
}

var authorCelCmd = &cobra.Command{
	Use:   "cel <expression>",
	Short: "work a CEL expression out as a formula or a rule of a key would: on values, facts and limits",
	Long: `Works the expression out as a formula, a rule or a suggestion of the key reads it — values (the
key's), facts (its target's), limits (env.yaml's keys), pack (the other keys) — on the values
rendered as far as they go: a check of an expression with no generating and no release.`,
	Args: cobra.ExactArgs(1),
	Run: run(func(_ *cobra.Command, args []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		b, err := bundle.Load(authorPack)
		if err != nil {
			return err
		}
		key := authorKey
		if len(key) == 0 {
			if len(b.Keys) != 1 {
				return fmt.Errorf("--key: the pack has the keys %s", strings.Join(b.Keys, ", "))
			}
			key = b.Keys[0]
		}
		var v render.Values
		if len(authorValues) > 0 {
			raw, err := os.ReadFile(authorValues)
			if err != nil {
				return err
			}
			if err := yaml.Unmarshal(raw, &v.Values); err != nil {
				return fmt.Errorf("%s: %w", authorValues, err)
			}
		}
		rc := render.Context{Installation: scenario.TestInstallation, Target: memory.New()}
		if len(authorFacts) > 0 {
			facts, err := scenario.FactsOf(authorFacts, func(p string) string { return p })
			if err != nil {
				return err
			}
			rc.Facts = map[string]map[string]any{}
			for name, f := range facts {
				rc.Facts[name] = f.Map()
			}
		}
		if len(authorEnv) > 0 {
			env, err := environment.Load(ctx, authorEnv, nil)
			if err != nil {
				return err
			}
			rc.Keys = session.Placements(env.Keys)
		}
		got, err := render.Eval(b, v, rc, key, args[0])
		if err != nil {
			return withCode(api.CodeValuesInvalid, err)
		}
		text, _ := json.Marshal(got)
		fmt.Println(string(text))
		return result(struct {
			Key   string `json:"key"`
			Value any    `json:"value"`
		}{key, got})
	}),
}

// refTopics are the topics of authoring.md: a section by its heading, or a paragraph by how it starts.
var refTopics = map[string]string{
	"struct":   "## The struct",
	"root":     "## PackRoot",
	"cel":      "## Formulas and rules",
	"suggest":  "A **suggestion**",
	"diagnose": "A **diagnosis**",
	"action":   "An **action**",
	"files":    "## Files",
	"packages": "## Installing from OS packages",
	"docs":     "## Documentation",
	"holds":    "## A pack that holds a pack",
	"generate": "## Generating",
	"test":     "## Testing",
}

var authorRefCmd = &cobra.Command{
	Use:   "ref <topic>",
	Short: "the part of the authoring guide a topic is: struct, root, cel, suggest, diagnose, action, files, packages, docs, holds, generate, test",
	Long: `Says the part of docs/authoring.md of a topic — a few hundred words instead of the whole guide,
for a model to read before it writes that part of a pack — with the code of --lang alone.`,
	Args: cobra.ExactArgs(1),
	Run: run(func(_ *cobra.Command, args []string) error {
		text, err := refOf(packdocs.Authoring, args[0], authorShowLang)
		if err != nil {
			return err
		}
		fmt.Print(text)
		return result(struct {
			Topic string `json:"topic"`
			Text  string `json:"text"`
		}{args[0], text})
	}),
}

var codeFence = regexp.MustCompile("^```(\\w*)")

// refOf is the topic's part of the guide, the code blocks of other languages left out.
func refOf(guide, topic, lang string) (string, error) {
	start, ok := refTopics[topic]
	if !ok {
		return "", fmt.Errorf("topic %q: one of %s", topic, strings.Join(slices.Sorted(maps.Keys(refTopics)), ", "))
	}
	lines := strings.Split(guide, "\n")
	from := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, start) })
	if from < 0 {
		return "", fmt.Errorf("topic %q: not in the guide", topic)
	}
	heading := strings.HasPrefix(start, "#")
	var out []string
	skip, inCode := false, false
	for i := from; i < len(lines); i++ {
		l := lines[i]
		if i > from && !inCode {
			if strings.HasPrefix(l, "## ") || strings.HasPrefix(l, "# ") {
				break
			}
			if !heading && (strings.HasPrefix(l, "A **") || strings.HasPrefix(l, "An **") || strings.HasPrefix(l, "The expression sees")) {
				break
			}
		}
		if m := codeFence.FindStringSubmatch(l); m != nil {
			if !inCode {
				inCode = true
				code := m[1]
				skip = len(code) > 0 && code != lang && slices.Contains(author.Langs, code)
			} else {
				inCode = false
				if skip {
					skip = false
					continue
				}
			}
		}
		if !skip {
			out = append(out, l)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n")) + "\n", nil
}

var authorExampleCmd = &cobra.Command{
	Use:   "example <postgres|openbao|docker>",
	Short: "an example pack's source in a language: postgres (hooks, packages, actions), openbao (a pack that holds one, formulas, rules), docker",
	Long: `Says the source of an example pack in --lang: the nearest to what is being written, as a model's
few shots.`,
	Args: cobra.ExactArgs(1),
	Run: run(func(_ *cobra.Command, args []string) error {
		dir := path.Join(args[0], authorShowLang)
		ents, err := fs.ReadDir(packexamples.Sources, dir)
		if err != nil {
			return fmt.Errorf("example %s in %s: there are postgres, openbao and docker, in go, python and java", args[0], authorShowLang)
		}
		files := map[string]string{}
		comment := "//"
		if authorShowLang == author.Python {
			comment = "#"
		}
		for _, e := range ents {
			if !packexamples.IsSource(e.Name()) {
				continue
			}
			b, err := fs.ReadFile(packexamples.Sources, path.Join(dir, e.Name()))
			if err != nil {
				return err
			}
			files[e.Name()] = string(b)
			fmt.Printf("%s %s\n%s\n", comment, e.Name(), b)
		}
		return result(struct {
			Example string            `json:"example"`
			Lang    string            `json:"lang"`
			Files   map[string]string `json:"files"`
		}{args[0], authorShowLang, files})
	}),
}

var authorDocsCmd = &cobra.Command{
	Use:   "docs [source-dir] -l <lang>",
	Short: "the docs/<lang>.yaml of a pack's source to fill: every field, group, rule, diagnosis and action",
	Long: `Generates the pack from its source and says docs/<lang>.yaml for it (pack docs skeleton): every
field, group, rule, diagnosis and action, with what the schema says in the language and ""
where it says nothing; a translation has the base language's text above each entry. Fill it
in, write it to docs/<lang>.yaml beside the source, run pack author check.`,
	Args: cobra.MaximumNArgs(1),
	Run: run(func(_ *cobra.Command, args []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		src := "."
		if len(args) > 0 {
			src = args[0]
		}
		src, err := filepath.Abs(src)
		if err != nil {
			return err
		}
		out, root, _, res, err := generateFrom(ctx, src)
		if len(out) > 0 {
			defer os.RemoveAll(out)
		}
		if err != nil {
			return err
		}
		if len(res.Generate) > 0 {
			return withCode(api.CodeCheckFailed, fmt.Errorf("the generator failed:\n%s", res.Generate))
		}
		raw, err := os.ReadFile(filepath.Join(out, root, format.SchemaFile))
		if err != nil {
			return err
		}
		s, err := schema.Parse(raw)
		if err != nil {
			return err
		}
		text := docs.Skeleton(s, docsLang)
		_, _ = os.Stdout.Write(text)
		return result(struct {
			Pack string `json:"pack"`
			Lang string `json:"lang"`
			Text string `json:"text"`
		}{root, docsLang, string(text)})
	}),
}

var authorScaffoldCmd = &cobra.Command{
	Use:   "scaffold --spec <spec.yaml> | --from <generated pack> [--lang L] [-o dir]",
	Short: "write a pack's source in a language from a short spec of it — or from a generated pack, to port it",
	Long: `Writes the source of a pack in --lang under -o from its spec: the struct of its settings, Meta,
PackRoot, its formulas, rules, suggestions, diagnoses and actions, as the examples are written
(Go: the package and cmd/main.go; Python: one file; Java: one class), docs/<lang>.yaml of each
language of the spec, content/ for the files the root names, tests/basic.yaml. Once: from then
on the source is the pack — run pack author check on it.

The spec is what a model gives instead of code, its output constrained by the spec's JSON
Schema (--schema). --from writes the spec of a generated pack instead: the pack written again,
in another language, its templates/ for content/.`,
	Args: cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		if scaffoldSchema {
			b, err := author.SpecSchema()
			if err != nil {
				return err
			}
			if jsonOut {
				return result(json.RawMessage(b))
			}
			fmt.Println(string(b))
			return nil
		}
		var spec *author.Spec
		var templates string
		switch {
		case len(scaffoldSpec) > 0 && len(scaffoldFrom) > 0:
			return errors.New("--spec or --from, not both")
		case len(scaffoldSpec) > 0:
			raw, err := os.ReadFile(scaffoldSpec)
			if err != nil {
				return err
			}
			spec = &author.Spec{}
			if err := yaml.UnmarshalStrict(raw, spec); err != nil {
				return withCode(api.CodeValuesInvalid, fmt.Errorf("%s: %w", scaffoldSpec, err))
			}
		case len(scaffoldFrom) > 0:
			var err error
			if spec, err = author.FromPack(scaffoldFrom); err != nil {
				return err
			}
			templates = filepath.Join(scaffoldFrom, "templates")
		default:
			return errors.New("--spec <spec.yaml> or --from <generated pack> (--schema says what a spec is)")
		}
		files, err := author.Scaffold(spec, scaffoldLang, scaffoldOut, author.ScaffoldOptions{GoImport: scaffoldGoImport, Templates: templates, Force: scaffoldForce})
		if err != nil {
			return withCode(api.CodeValuesInvalid, err)
		}
		for _, f := range files {
			fmt.Println("wrote", filepath.Join(scaffoldOut, f))
		}
		fmt.Printf("next: pack author check %s\n", scaffoldOut)
		return result(struct {
			Lang  string   `json:"lang"`
			Dir   string   `json:"dir"`
			Files []string `json:"files"`
		}{scaffoldLang, scaffoldOut, files})
	}),
}
