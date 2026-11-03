package packschema

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/invopop/jsonschema"
)

// Defaulter sets the defaults of a component that its tags cannot say: SetDefaults is called on the value Reflect is
// given — a pointer to a zero struct — before the schema is read off it. It must set, not add: it may run more than
// once.
type Defaulter interface{ SetDefaults() }

// Rooter gives the root annotation of a component: x-pack.
type Rooter interface{ PackRoot() Root }

// Computer gives the formulas, keyed by JSON Pointer relative to the component
// root. CEL does not live in a struct tag: quotes and commas.
type Computer interface{ PackCompute() map[string]string }

// Ruler gives the validation rules, keyed by JSON Pointer like the formulas:
// every rule of the list hangs on the node as x-pack-rules and must hold of
// the values — see Rule.
type Ruler interface{ PackRules() map[string][]Rule }

// Resolver says whether a Go type is a component of the pack and what it is called; the schema refers to it
// as pack://<name>#. A nil Resolver finds none.
type Resolver func(t reflect.Type) (name string, ok bool)

// Options of Reflect.
type Options struct {
	// Components finds the fields that are components of their own.
	Components Resolver
}

// Reflect turns a Go default value into the component's schema. The struct is the application's own
// configuration: the json tags name the properties and a field without omitempty is required; an env tag makes the
// field an environment variable. The VALUE, after SetDefaults, is the defaults document: a field that is not zero
// is a setting with a default.
//
// The tags a field may carry besides json:
//
//	env:"NAME"        the field is the environment variable NAME
//
// A field whose type is a pack becomes a reference to its schema, and its settings join the parent's. The ports
// and volumes the application has, and how it starts, upgrades and rolls back, are not the struct's: PackRoot
// says them.
func Reflect(defaults any, o Options) (*jsonschema.Schema, error) {
	if d, ok := defaults.(Defaulter); ok {
		d.SetDefaults()
	}
	r := jsonschema.Reflector{
		Anonymous:      true,
		ExpandedStruct: true,
		DoNotReference: true,
	}
	s := r.Reflect(defaults)
	s.Version = jsonschema.Version

	if root, ok := defaults.(Rooter); ok {
		extras(s)["x-pack"] = root.PackRoot()
	}
	if err := annotate(s, reflect.ValueOf(defaults), "", o); err != nil {
		return nil, err
	}
	if c, ok := defaults.(Computer); ok {
		for ptr, expr := range c.PackCompute() {
			node, err := lookup(s, ptr)
			if err != nil {
				return nil, fmt.Errorf("x-pack-compute %s: %w", ptr, err)
			}
			extras(node)["x-pack-compute"] = expr
		}
	}
	if c, ok := defaults.(Ruler); ok {
		ids := map[string]string{}
		all := c.PackRules()
		for _, ptr := range slices.Sorted(maps.Keys(all)) { // in order: the same pair named every run
			rules := all[ptr]
			for _, r := range rules {
				if len(r.ID) == 0 {
					continue
				}
				if other, dup := ids[r.ID]; dup {
					return nil, fmt.Errorf("x-pack-rules: the id %q is of a rule on %s and on %s", r.ID, other, ptr)
				}
				ids[r.ID] = ptr
			}
			node, err := lookup(s, ptr)
			if err != nil {
				return nil, fmt.Errorf("x-pack-rules %s: %w", ptr, err)
			}
			extras(node)["x-pack-rules"] = slices.Clone(rules) // the schema's own: docs writes the messages into it
		}
	}
	return s, nil
}

func annotate(s *jsonschema.Schema, v reflect.Value, path string, o Options) error {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct || s.Properties == nil {
		return nil
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Anonymous && len(jsonTagName(f)) == 0 {
			// an embedded struct: its fields are promoted into this schema
			// (jsonschema inlines it), so its tags are annotated at this level
			if err := annotate(s, v.Field(i), path, o); err != nil {
				return err
			}
			continue
		}
		name := jsonName(f)
		if len(name) == 0 {
			continue
		}
		prop, ok := s.Properties.Get(name)
		if !ok {
			return fmt.Errorf("%s/%s: not in the reflected schema", path, name)
		}
		fv := v.Field(i)
		// a component of its own: a reference to its schema
		if o.Components != nil {
			if comp, ok := o.Components(f.Type); ok {
				if len(path) > 0 {
					return fmt.Errorf("%s/%s: a pack is held by a field of the pack itself, not of a struct inside it: its settings join the schema as a property of the root", path, name)
				}
				repl := &jsonschema.Schema{Ref: PackScheme + comp + "#"}
				if !fv.IsZero() {
					repl.Default = prune(fv.Interface())
				}
				s.Properties.Set(name, repl)
				continue
			}
		}

		if leafWithDefault(fv) {
			prop.Default = fv.Interface()
		}
		if err := refuseFileTags(f); err != nil {
			return fmt.Errorf("%s/%s: %w", path, name, err)
		}
		if env := f.Tag.Get("env"); len(env) > 0 && env != "-" {
			extras(prop)["x-pack-env"] = env
		}
		if err := annotate(prop, fv, path+"/"+name, o); err != nil {
			return err
		}
	}
	return nil
}

// leafWithDefault: a field SetDefaults (or the author) set to something is a setting with that default. A struct
// is not one itself — its fields are — and neither is a secret.
func leafWithDefault(fv reflect.Value) bool {
	t := fv.Type()
	for t.Kind() == reflect.Pointer {
		if fv.IsNil() {
			return false
		}
		t, fv = t.Elem(), fv.Elem()
	}
	if t.Kind() == reflect.Struct || fv.IsZero() {
		return false
	}
	_, isSecret := fv.Interface().(Secret)
	return !isSecret
}

// prune drops zero values — "", 0, false, nil, empty objects and arrays —
// from a value after marshalling it, leaving what the author set.
func prune(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return v
	}
	out, _ := pruneValue(doc)
	return out
}

func pruneValue(v any) (any, bool) {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if pe, keep := pruneValue(e); keep {
				out[k] = pe
			}
		}
		return out, len(out) > 0
	case []any:
		return x, len(x) > 0
	case string:
		return x, len(x) > 0
	case float64:
		return x, x != 0
	case bool:
		return x, x
	case nil:
		return nil, false
	}
	return v, true
}

func lookup(s *jsonschema.Schema, ptr string) (*jsonschema.Schema, error) {
	node := s
	for seg := range strings.SplitSeq(strings.TrimPrefix(ptr, "/"), "/") {
		if node.Properties == nil {
			return nil, fmt.Errorf("%q: not an object", seg)
		}
		next, ok := node.Properties.Get(seg)
		if !ok {
			return nil, fmt.Errorf("%q: no such property", seg)
		}
		node = next
	}
	return node, nil
}

func extras(s *jsonschema.Schema) map[string]any {
	if s.Extras == nil {
		s.Extras = map[string]any{}
	}
	return s.Extras
}

// jsonTagName is the name part of the json tag, "" when absent.
func jsonTagName(f reflect.StructField) string {
	tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	return tag
}

func jsonName(f reflect.StructField) string {
	tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if tag == "-" {
		return ""
	}
	if len(tag) == 0 {
		return f.Name
	}
	return tag
}

// JSONSchema makes a Secret leaf reflect as a password string.
func (Secret) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Format: "password"}
}

// refuseFileTags: the files of a pack are in PackRoot, a tag left over from before says nothing.
func refuseFileTags(f reflect.StructField) error {
	for _, t := range []string{"file", "template", "format", "mode", "parseAs"} {
		if len(f.Tag.Get(t)) > 0 {
			return fmt.Errorf("%s: a %s tag: the files of a pack are in the Content of PackRoot", f.Name, t)
		}
	}
	return nil
}

// FormatOf is the format a file's name says: .json, .yaml, .properties …
func FormatOf(dst string) string {
	switch strings.ToLower(filepath.Ext(dst)) {
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".properties":
		return "properties"
	case ".toml":
		return "toml"
	case ".ini", ".cnf":
		return "ini"
	case ".env":
		return "env"
	}
	return ""
}
