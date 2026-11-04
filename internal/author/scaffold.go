package author

import (
	"bufio"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/invopop/jsonschema"
	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/sdk/docs"
)

// SpecSchema is the JSON Schema of a Spec: what a model's output is constrained by.
func SpecSchema() ([]byte, error) {
	r := jsonschema.Reflector{ExpandedStruct: true, DoNotReference: false}
	return json.MarshalIndent(r.Reflect(&Spec{}), "", "  ")
}

// ScaffoldOptions are what writing a source needs besides the spec.
type ScaffoldOptions struct {
	// GoImport is the import path of the Go package written: by default the module of the go.mod above the directory,
	// and the directory's path in it.
	GoImport string
	// Templates is where the files of content/ come from (a generated pack's templates/); none — a stub of each.
	Templates string
	// Force writes over the files already there; without it a file there is refused before any is written — a
	// scaffold is written once, and from then on the source is the author's.
	Force bool
}

// Scaffold writes the source of the spec in lang under dir: the pack's code, docs/<lang>.yaml of each language,
// content/ the root names, tests/basic.yaml — what pack author check runs. The files written, by path.
func Scaffold(s *Spec, lang, dir string, o ScaffoldOptions) ([]string, error) {
	if err := s.Check(); err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	var order []string
	put := func(rel string, b []byte) error {
		order = append(order, rel)
		files[rel] = b
		return nil
	}
	switch lang {
	case Go:
		imp := o.GoImport
		if len(imp) == 0 {
			var err error
			if imp, err = goImport(dir); err != nil {
				return nil, err
			}
		}
		pack, main, err := s.GoSource(imp)
		if err != nil {
			return nil, err
		}
		if err := put(strings.ReplaceAll(s.Name, "-", "_")+".go", pack); err != nil {
			return nil, err
		}
		if err := put(filepath.Join("cmd", "main.go"), main); err != nil {
			return nil, err
		}
	case Python:
		if err := put(strings.ReplaceAll(s.Name, "-", "_")+".py", s.PythonSource()); err != nil {
			return nil, err
		}
	case Java:
		root, _, _ := s.types()
		if err := put(root.name+".java", s.JavaSource()); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("--lang %q: one of %s", lang, strings.Join(Langs, ", "))
	}
	ds, err := s.Docs()
	if err != nil {
		return nil, err
	}
	for _, l := range slices.Sorted(maps.Keys(ds)) {
		if err := put(filepath.Join(docs.Dir, l+".yaml"), ds[l]); err != nil {
			return nil, err
		}
	}
	for _, name := range s.contentNames() {
		b := []byte("# " + name + ": the file of the pack, to write\n")
		if len(o.Templates) > 0 {
			if got, err := os.ReadFile(filepath.Join(o.Templates, name)); err == nil {
				b = got
			}
		}
		if err := put(filepath.Join("content", name), b); err != nil {
			return nil, err
		}
	}
	sc, err := s.scenario()
	if err != nil {
		return nil, err
	}
	if err := put(filepath.Join("tests", "basic.yaml"), sc); err != nil {
		return nil, err
	}
	if !o.Force {
		var there []string
		for _, rel := range order {
			if _, err := os.Lstat(filepath.Join(dir, rel)); err == nil {
				there = append(there, rel)
			}
		}
		if len(there) > 0 {
			return nil, fmt.Errorf("%s holds %s already: a scaffold is written once — another directory, or --force to write over them",
				dir, strings.Join(there, ", "))
		}
	}
	for _, rel := range order {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, files[rel], 0o644); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// contentNames are the files of content/ the root names: sources and templates, of the image and of the OSes.
func (s *Spec) contentNames() []string {
	seen := map[string]bool{}
	add := func(n string) {
		if len(n) > 0 {
			seen[n] = true
		}
	}
	for _, f := range s.Root.Content {
		add(f.Source)
		add(f.Template)
	}
	for _, p := range s.Root.Packages {
		for _, f := range append(slices.Clone(p.Repo), p.Content...) {
			add(f.Source)
			add(f.Template)
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// scenario is tests/basic.yaml: every required setting given a value of its type, the least the pack runs on given
// as the environment's requests, and an apply that installs — what to change first.
func (s *Spec) scenario() ([]byte, error) {
	var values func(fs []Field) map[string]any
	values = func(fs []Field) map[string]any {
		out := map[string]any{}
		for _, f := range fs {
			if !f.Required {
				continue
			}
			switch f.Type {
			case TObject:
				if v := values(f.Fields); len(v) > 0 {
					out[f.Name] = v
				}
			case TSecret:
				out[f.Name] = map[string]any{"secretRef": map[string]any{"name": s.Name + "-" + f.Name, "key": f.Name}}
			case TInteger, TNumber:
				out[f.Name] = 1
			case TBoolean:
				out[f.Name] = true
			case TList:
				out[f.Name] = []any{}
			case TMap:
				out[f.Name] = map[string]any{}
			default:
				out[f.Name] = s.Name
			}
		}
		return out
	}
	sc := map[string]any{"name": s.Name + " installs with its required settings given", "values": values(s.Fields),
		"expect": map[string]any{"problems": []any{}, "apply": map[string]any{"state": "installed", "in_effect": []any{s.Name}}}}
	if len(s.Root.Minimum) > 0 {
		sc["env"] = map[string]any{"keys": map[string]any{s.Name: map[string]any{"resources": map[string]any{"requests": s.Root.Minimum}}}}
	}
	return yaml.Marshal(sc)
}

// goImport is the import path of a directory: the module of the go.mod above it, and the directory's path in it.
func goImport(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for d := abs; ; d = filepath.Dir(d) {
		f, err := os.Open(filepath.Join(d, "go.mod"))
		if err == nil {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				if mod, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
					_ = f.Close()
					rel, err := filepath.Rel(d, abs)
					if err != nil {
						return "", err
					}
					if rel == "." {
						return strings.TrimSpace(mod), nil
					}
					return strings.TrimSpace(mod) + "/" + filepath.ToSlash(rel), nil
				}
			}
			err := sc.Err()
			_ = f.Close()
			if err != nil {
				return "", fmt.Errorf("%s: %w", filepath.Join(d, "go.mod"), err)
			}
		}
		if parent := filepath.Dir(d); parent == d {
			return "", fmt.Errorf("%s is in no Go module: --go-import names the package's import path", dir)
		}
	}
}
