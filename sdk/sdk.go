// Package sdk is what an author of a pack writes against. A pack describes at least one application: its
// configuration (a struct whose fields carry json and env tags — what a field is), and how to run it, upgrade it
// and roll it back (PackRoot). A pack may hold another pack in a field; the settings of the two join in one JSON
// schema, and the order they are installed in is the order of the fields' containment.
//
//	type OpenBao struct {
//	    Bao      Config        `json:"openbao" file:"/etc/openbao/config.json"`
//	    Postgres *tembo.Config `json:"postgres,omitempty"` // a pack of its own; optional: a pointer
//	}
//
//	func (*OpenBao) SetDefaults()            {}
//	func (*OpenBao) Meta() sdk.Meta           { return sdk.Meta{Name: "openbao", Version: "2.1.0", Image: "oci://images/openbao:2.1.0"} }
//	func (*OpenBao) PackRoot() packschema.Root { return packschema.Root{Ports: …, Readiness: …} }
//
// The files a pack ships are in its content/ directory, next to the source; fields name them with the template tag
// and say where they go with the file tag. Generate writes each pack's directory — schema.json, pack.yaml,
// templates/ — Verify runs the author checks in a
// test, Run is a ready generator CLI.
package sdk

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	packer "github.com/arenadata/oci-packer/types"
	"github.com/horchestra/pack/sdk/check"
	packschema2 "github.com/horchestra/pack/sdk/packschema"
	"github.com/horchestra/pack/sdk/schema"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/pkg/format"
)

// Meta is everything about a pack that goes into its OCI artifact. Name and Version are required; the rest is
// written to the artifact's annotations as the author gives it, under the names the OCI image spec uses
// (org.opencontainers.image.*) beside the pack's own (pack.name, pack.version).
type Meta struct {
	Name    string // a DNS label; the key of the artifact among the packs of a pack
	Version string

	Description   string
	Documentation string // a URL
	Source        string // a URL of the source
	URL           string // a URL of the project
	License       string // an SPDX expression
	Vendor        string
	Authors       string
	Created       string // RFC 3339; empty — not written, so that the artifact is the same byte for byte

	Image string // oci://repo:tag the application runs — a repository of the registry the pack is pushed to, no host; "" — the pack runs nothing itself
	// Artifact is oci://repo where the pack is published (no tag, no host): what a pack that holds it mounts it from.
	Artifact string
	// Annotations are any more; a name the sdk writes itself is refused.
	Annotations map[string]string
	// Dir is the pack's directory, whose content/ holds its files. Empty — the directory of the source file
	// that defines the pack's Meta method.
	Dir string
}

// Pack is a pack type: a struct whose pointer implements the three methods. The schema is read off new(T) after
// SetDefaults.
//
//	SetDefaults()   what the type cannot say by being zero; sets, never adds
//	Meta()          the identity: name, version, image
//	PackRoot()      how it runs, upgrades and rolls back: ports, volumes, readiness, hooks, savepoints …
//
// PackCompute (the formulas) and PackRules (the validation rules) are optional — see packschema.
type Pack interface {
	SetDefaults()
	Meta() Meta
	PackRoot() packschema2.Root
}

// Secret is a credential leaf: the operator gives a reference to a Secret, never a value.
type Secret = packschema2.Secret

// ---- the packs a pack holds

// Packs lists the pack and every pack it holds in its fields — optional ones, nil, too — each once, the pack
// first, then the packs it holds. The artifact of an absent optional pack is still part of the pack's: the
// operator may name it in the values. A pack that holds itself, however far down, is refused.
func Packs(root Pack) ([]Pack, error) {
	var out []Pack
	seen := map[string]bool{}
	var walk func(p Pack, path []string) error
	walk = func(p Pack, path []string) error {
		name := p.Meta().Name
		if len(name) == 0 {
			return fmt.Errorf("%T: Meta needs a Name", p)
		}
		if slices.Contains(path, name) {
			return fmt.Errorf("pack %q holds itself: %s", name, strings.Join(append(slices.Clone(path), name), " → "))
		}
		if seen[name] {
			return nil
		}
		seen[name] = true
		out = append(out, p)
		for _, sub := range holds(p) {
			if err := walk(sub, append(slices.Clone(path), name)); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, nil); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- the artifact

// Schema is the pack's schema.json, stable byte for byte.
func Schema(p Pack) ([]byte, error) {
	s, err := packschema2.Reflect(p, packschema2.Options{Components: func(t reflect.Type) (string, bool) {
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return "", false
		}
		sub, ok := reflect.New(t).Interface().(Pack)
		if !ok {
			return "", false
		}
		return sub.Meta().Name, true
	}})
	if err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

// holds lists the packs a pack holds directly, by their field's type.
func holds(p Pack) []Pack {
	var out []Pack
	t := reflect.TypeOf(p)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	for f := range t.Fields() {
		if !f.IsExported() || f.Anonymous {
			continue
		}
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() != reflect.Struct {
			continue
		}
		if sub, ok := reflect.New(ft).Interface().(Pack); ok {
			out = append(out, sub)
		}
	}
	return out
}

// PackFile is the artifact's pack file as oci-packer's own type: the pack's image (a mount), its schema, its
// templates, and the artifact of every pack it holds.
func PackFile(p Pack) (*packer.Pack, error) {
	m := p.Meta()
	if len(m.Name) == 0 || len(m.Version) == 0 {
		return nil, fmt.Errorf("%T: Meta needs Name and Version", p)
	}
	ann, err := m.annotations()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.Name, err)
	}
	pf := &packer.Pack{Type: format.ArtifactPack, Annotations: ann}
	if len(m.Image) > 0 {
		pf.Items = append(pf.Items, packer.Descriptor{From: m.Image, Annotations: map[string]string{format.AnnotationImage: "true"}})
	}
	pf.Items = append(pf.Items, packer.Descriptor{From: "file://" + format.SchemaFile, Type: format.ArtifactSchema})
	if n, err := content(p); err != nil {
		return nil, fmt.Errorf("%s: %w", m.Name, err)
	} else if len(n) > 0 {
		pf.Items = append(pf.Items, packer.Descriptor{From: "dir://" + format.TemplatesDir + "/", Type: format.ArtifactTemplate})
	}
	// every pack the pack holds, those they hold too, each once: a loader finds any by name in the one index
	all, err := Packs(p)
	if err != nil {
		return nil, err
	}
	for _, sub := range all[1:] {
		sm := sub.Meta()
		if len(sm.Artifact) == 0 {
			return nil, fmt.Errorf("%s holds %s, which has no Artifact (oci://repo) to mount it from", m.Name, sm.Name)
		}
		if len(sm.Version) == 0 {
			return nil, fmt.Errorf("%s: Meta needs a Version", sm.Name)
		}
		pf.Items = append(pf.Items, packer.Descriptor{From: sm.Artifact + ":" + sm.Version})
	}
	return pf, nil
}

// PackFileYAML is PackFile as the bytes of pack.yaml.
func PackFileYAML(p Pack) ([]byte, error) {
	pf, err := PackFile(p)
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(pf)
}

// Generate writes every pack's directory under dir: <name>/schema.json, <name>/pack.yaml and
// <name>/templates/*. The packs a pack holds are written too, so one call writes the whole tree oci-packer packs.
func Generate(dir string, roots ...Pack) error {
	seen := map[string]bool{}
	var all []Pack
	for _, r := range roots {
		ps, err := Packs(r)
		if err != nil {
			return err
		}
		for _, p := range ps {
			if !seen[p.Meta().Name] {
				seen[p.Meta().Name] = true
				all = append(all, p)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Meta().Name < all[j].Meta().Name })
	for _, p := range all {
		m := p.Meta()
		out := filepath.Join(dir, m.Name)
		if err := os.MkdirAll(out, 0o755); err != nil {
			return err
		}
		s, err := Schema(p)
		if err != nil {
			return fmt.Errorf("%s: %w", m.Name, err)
		}
		if err = os.WriteFile(filepath.Join(out, format.SchemaFile), s, 0o644); err != nil {
			return err
		}
		pf, err := PackFileYAML(p)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(out, format.PackFile), pf, 0o644); err != nil {
			return err
		}
		ts, err := content(p)
		if err != nil {
			return fmt.Errorf("%s: content: %w", m.Name, err)
		}
		for name, b := range ts {
			path := filepath.Join(out, format.TemplatesDir, name)
			if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err = os.WriteFile(path, b, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// Verify runs the author checks on a pack as generated: the schema, its templates, the packs it holds. Meant for
// the author's tests.
func Verify(p Pack) ([]check.Problem, error) {
	raw, err := Schema(p)
	if err != nil {
		return nil, err
	}
	s, err := schema.Parse(raw)
	if err != nil {
		return nil, err
	}
	// the packs it holds are checked on their own: here they are open, whatever they are
	for _, sub := range holds(p) {
		open, err := schema.Parse([]byte(`{"type":"object"}`))
		if err != nil {
			return nil, err
		}
		open.Foreign = s.Foreign
		s.Attach(packschema2.PackScheme+sub.Meta().Name, open)
	}
	ts, err := content(p)
	if err != nil {
		return nil, err
	}
	return check.Run(check.Component{Name: p.Meta().Name, Schema: s, Templates: ts})
}

// Command is the generator CLI of an author's main package: a cobra command with one subcommand per job, to which
// the author adds his own (the values of a stand, a graph) before running it.
//
//	generate [-o dir]   write every pack's directory (default .)
//	check               run the author checks on every pack
//	schema <name>       print one pack's schema.json
//
//	func main() {
//	    cmd := sdk.Command(&openbao.OpenBao{})
//	    cmd.AddCommand(valuesCmd)
//	    sdk.Execute(cmd)
//	}
func Command(roots ...Pack) *cobra.Command {
	root := &cobra.Command{Use: filepath.Base(os.Args[0]), Short: "generate and check the artifacts of the packs", SilenceErrors: true}

	// the packs, by name, once the first command needs them
	var byName map[string]Pack
	var names []string
	load := func() error {
		if byName != nil {
			return nil
		}
		byName = map[string]Pack{}
		for _, r := range roots {
			ps, err := Packs(r)
			if err != nil {
				return err
			}
			for _, p := range ps {
				if _, ok := byName[p.Meta().Name]; !ok {
					byName[p.Meta().Name] = p
					names = append(names, p.Meta().Name)
				}
			}
		}
		sort.Strings(names)
		return nil
	}

	var out string
	generate := &cobra.Command{
		Use: "generate", Short: "write every pack's directory: schema.json, pack.yaml, templates/", Args: cobra.NoArgs,
		Run: run(func(*cobra.Command, []string) error {
			if err := load(); err != nil {
				return err
			}
			if err := Generate(out, roots...); err != nil {
				return err
			}
			for _, n := range names {
				fmt.Println("wrote", filepath.Join(out, n))
			}
			return nil
		}),
	}
	generate.Flags().StringVarP(&out, "output", "o", ".", "directory to write the artifacts under")

	checkCmd := &cobra.Command{
		Use: "check", Short: "run the author checks on every pack", Args: cobra.NoArgs,
		Run: run(func(*cobra.Command, []string) error {
			if err := load(); err != nil {
				return err
			}
			problems := 0
			for _, n := range names {
				ps, err := Verify(byName[n])
				if err != nil {
					return fmt.Errorf("%s: %w", n, err)
				}
				for _, p := range ps {
					fmt.Printf("%s: %s\n", n, p)
					problems++
				}
			}
			if problems > 0 {
				return fmt.Errorf("%d problem(s)", problems)
			}
			fmt.Println("ok")
			return nil
		}),
	}

	schemaCmd := &cobra.Command{
		Use: "schema <name>", Short: "print one pack's schema.json", Args: cobra.ExactArgs(1),
		Run: run(func(_ *cobra.Command, args []string) error {
			if err := load(); err != nil {
				return err
			}
			p, ok := byName[args[0]]
			if !ok {
				return fmt.Errorf("no pack %q (have %s)", args[0], strings.Join(names, ", "))
			}
			s, err := Schema(p)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(s)
			return err
		}),
	}
	root.AddCommand(generate, checkCmd, schemaCmd)
	return root
}

// Execute runs the command; an error, its own or cobra's (an unknown command, a missing argument), is said as
// "<name>: …" and the process exits with 1.
func Execute(cmd *cobra.Command) {
	if err := cmd.Execute(); err != nil {
		Fail(err)
	}
}

// Run is Execute over Command, for a main that has nothing to add.
func Run(roots ...Pack) { Execute(Command(roots...)) }

// run makes the Run of a command whose work can fail: the error is said and the process exits. A command that
// returns an error to cobra (RunE) makes cobra print the usage after it, as if an argument were wrong.
func run(f func(*cobra.Command, []string) error) func(*cobra.Command, []string) {
	return func(c *cobra.Command, args []string) {
		if err := f(c, args); err != nil {
			Fail(err)
		}
	}
}

// Fail says err as "<name>: …" on stderr and exits with 1.
func Fail(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "%s: %v\n", filepath.Base(os.Args[0]), err)
	os.Exit(1)
}
