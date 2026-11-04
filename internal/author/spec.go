package author

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/pkg/format"
	"github.com/horchestra/pack/sdk/docs"
	"github.com/horchestra/pack/sdk/packschema"
	"github.com/horchestra/pack/sdk/schema"
)

// Spec is a pack said short: what pack author scaffold writes the source of in a language — the struct of its
// settings, its Meta, its PackRoot and the rest, docs/<lang>.yaml, content/ to fill, a scenario. A model gives it as
// output constrained by its JSON Schema (SpecSchema) rather than code; a person writes it by hand. Once written, the
// source is what the pack is: the spec is not kept in step with it.
type Spec struct {
	Name          string            `json:"name" jsonschema:"required,pattern=^[a-z][a-z0-9-]*$"`
	Version       string            `json:"version" jsonschema:"required"`
	Image         string            `json:"image,omitempty" jsonschema:"description=repo:tag the application runs with no host"`
	Description   string            `json:"description,omitempty"`
	Documentation string            `json:"documentation,omitempty"`
	Source        string            `json:"source,omitempty"`
	URL           string            `json:"url,omitempty"`
	License       string            `json:"license,omitempty"`
	Vendor        string            `json:"vendor,omitempty"`
	Authors       string            `json:"authors,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`

	// Title and Description document the pack itself (docs/<lang>.yaml's /), I18n in the other languages.
	Title  string          `json:"title,omitempty"`
	Doc    string          `json:"doc,omitempty" jsonschema:"description=the pack's description in the documentation"`
	I18n   map[string]Text `json:"i18n,omitempty"`
	Langs  []string        `json:"langs,omitempty" jsonschema:"description=the languages of docs/ the base first; none — en"`
	Groups []Group         `json:"groups,omitempty"`

	Fields []Field `json:"fields" jsonschema:"required"`

	Root     packschema.Root               `json:"root"`
	Compute  map[string]string             `json:"compute,omitempty" jsonschema:"description=CEL formulas by the JSON Pointer of the field they fill"`
	Rules    map[string][]packschema.Rule  `json:"rules,omitempty" jsonschema:"description=CEL rules by the JSON Pointer of the field they are on"`
	Suggest  map[string]packschema.Suggest `json:"suggest,omitempty"`
	Diagnose []packschema.Diagnosis        `json:"diagnose,omitempty"`
	Actions  []Action                      `json:"actions,omitempty"`
}

// Text is a title and a description in a language.
type Text struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// Group is a group of settings, its title in the base language and the others.
type Group struct {
	ID    string            `json:"id" jsonschema:"required"`
	Title string            `json:"title" jsonschema:"required"`
	I18n  map[string]string `json:"i18n,omitempty"`
}

// The types of a field.
const (
	TString  = "string"
	TInteger = "integer"
	TNumber  = "number"
	TBoolean = "boolean"
	TObject  = "object" // Fields
	TList    = "list"   // of Items
	TMap     = "map"    // of Values, by string
	TSecret  = "secret" // a reference to a Secret: never a value
)

// Field is one setting: its json name, its type, whether the schema requires it, the environment variable it is,
// its default, its documentation.
type Field struct {
	Name     string `json:"name" jsonschema:"required,pattern=^[a-z][a-z0-9_-]*$"`
	Type     string `json:"type" jsonschema:"required,enum=string,enum=integer,enum=number,enum=boolean,enum=object,enum=list,enum=map,enum=secret"`
	Items    string `json:"items,omitempty" jsonschema:"description=list: the type of an item (string integer number boolean)"`
	Values   string `json:"values,omitempty" jsonschema:"description=map: the type of a value (string integer number boolean any)"`
	Required bool   `json:"required,omitempty"`
	Env      string `json:"env,omitempty"`
	Default  any    `json:"default,omitempty"`

	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	Examples    []any           `json:"examples,omitempty"`
	Group       string          `json:"group,omitempty"`
	I18n        map[string]Text `json:"i18n,omitempty"`

	Fields []Field `json:"fields,omitempty" jsonschema:"description=object: its fields"`
}

// Action is an operation of the pack (pack run), its parameters in the order the author gives them.
type Action struct {
	Name        string            `json:"name" jsonschema:"required"`
	Description string            `json:"description,omitempty"`
	I18n        map[string]string `json:"i18n,omitempty"`
	Command     []string          `json:"command" jsonschema:"required"`
	Timeout     string            `json:"timeout,omitempty"`
	Mutating    bool              `json:"mutating,omitempty"`
	Approve     bool              `json:"approve,omitempty"`
	Params      []Param           `json:"params,omitempty"`
}

// Param is a parameter of an action.
type Param struct {
	Name        string            `json:"name" jsonschema:"required"`
	Type        string            `json:"type" jsonschema:"required,enum=string,enum=integer,enum=number,enum=boolean"`
	Required    bool              `json:"required,omitempty"`
	Default     any               `json:"default,omitempty"`
	Enum        []any             `json:"enum,omitempty"`
	Description string            `json:"description,omitempty"`
	I18n        map[string]string `json:"i18n,omitempty"`
}

var fieldName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Check says what of the spec cannot be written: a field without a name or a type of the list, a pointer of a formula
// or a rule that names no field.
func (s *Spec) Check() error {
	if len(s.Name) == 0 || len(s.Version) == 0 {
		return fmt.Errorf("spec: a name and a version")
	}
	var walk func(prefix string, fs []Field) error
	walk = func(prefix string, fs []Field) error {
		seen := map[string]bool{}
		for _, f := range fs {
			ptr := prefix + "/" + f.Name
			if !fieldName.MatchString(f.Name) {
				return fmt.Errorf("spec: %s: a name of lower case letters, digits, underscores and dashes, a letter first", ptr)
			}
			if seen[f.Name] {
				return fmt.Errorf("spec: %s twice", ptr)
			}
			seen[f.Name] = true
			if len(f.Group) > 0 && !slices.ContainsFunc(s.Groups, func(g Group) bool { return g.ID == f.Group }) {
				return fmt.Errorf("spec: %s: group %q is not one of the spec's groups", ptr, f.Group)
			}
			if f.Default != nil {
				items := f.Items
				if f.Type == TMap {
					items = f.Values
				}
				if f.Type == TObject || f.Type == TSecret || !fits(f.Type, items, f.Default) {
					return fmt.Errorf("spec: %s: the default %v is not a %s", ptr, f.Default, f.Type)
				}
			}
			switch f.Type {
			case TString, TInteger, TNumber, TBoolean, TSecret:
			case TObject:
				if len(f.Fields) == 0 {
					return fmt.Errorf("spec: %s: an object has fields", ptr)
				}
				if err := walk(ptr, f.Fields); err != nil {
					return err
				}
			case TList:
				if !slices.Contains([]string{TString, TInteger, TNumber, TBoolean}, f.Items) {
					return fmt.Errorf("spec: %s: items is string, integer, number or boolean", ptr)
				}
			case TMap:
				if !slices.Contains([]string{TString, TInteger, TNumber, TBoolean, "any"}, f.Values) {
					return fmt.Errorf("spec: %s: values is string, integer, number, boolean or any", ptr)
				}
			default:
				return fmt.Errorf("spec: %s: type %q is not one of string integer number boolean object list map secret", ptr, f.Type)
			}
		}
		return nil
	}
	if err := walk("", s.Fields); err != nil {
		return err
	}
	ptrs := slices.Collect(maps.Keys(s.Compute))
	ptrs = append(ptrs, slices.Collect(maps.Keys(s.Rules))...)
	ptrs = append(ptrs, slices.Collect(maps.Keys(s.Suggest))...)
	for _, d := range s.Diagnose {
		for _, f := range d.Fix {
			if len(f.Patch) > 0 {
				ptrs = append(ptrs, f.Patch)
			}
		}
	}
	for _, p := range ptrs {
		if p == "/" || len(strings.Trim(p, "/")) == 0 {
			return fmt.Errorf("spec: %q: a formula, a rule or a fix of the whole pack is not said by a spec yet — name a field", p)
		}
		if _, ok := s.FieldPath(p); !ok {
			return fmt.Errorf("spec: %s names no field", p)
		}
	}
	actions := map[string]bool{}
	for _, a := range s.Actions {
		if len(a.Name) == 0 || len(a.Command) == 0 {
			return fmt.Errorf("spec: action %q: a name and a command", a.Name)
		}
		if actions[a.Name] {
			return fmt.Errorf("spec: action %s twice", a.Name)
		}
		actions[a.Name] = true
		params := map[string]bool{}
		for _, p := range a.Params {
			at := "action " + a.Name + ": param " + p.Name
			if len(p.Name) == 0 || params[p.Name] {
				return fmt.Errorf("spec: action %s: a param without a name or twice: %q", a.Name, p.Name)
			}
			params[p.Name] = true
			if !slices.Contains([]string{TString, TInteger, TNumber, TBoolean}, p.Type) {
				return fmt.Errorf("spec: %s: type %q is not one of string integer number boolean", at, p.Type)
			}
			if p.Default != nil && !fits(p.Type, "", p.Default) {
				return fmt.Errorf("spec: %s: the default %v is not a %s", at, p.Default, p.Type)
			}
			for _, e := range p.Enum {
				if !fits(p.Type, "", e) {
					return fmt.Errorf("spec: %s: %v of enum is not a %s", at, e, p.Type)
				}
			}
		}
	}
	return nil
}

// fits says whether a value, as JSON reads it, is of a type of a spec; items is what a list or a map holds.
func fits(typ, items string, v any) bool {
	switch typ {
	case TString:
		_, ok := v.(string)
		return ok
	case TBoolean:
		_, ok := v.(bool)
		return ok
	case TNumber, TInteger:
		var n float64
		switch x := v.(type) {
		case float64:
			n = x
		case int:
			n = float64(x)
		case int64:
			n = float64(x)
		default:
			return false
		}
		return typ == TNumber || n == math.Trunc(n)
	case TList:
		l, ok := v.([]any)
		if !ok {
			return false
		}
		for _, e := range l {
			if !fits(items, "", e) {
				return false
			}
		}
		return true
	case TMap:
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		for _, e := range m {
			if items != "any" && !fits(items, "", e) {
				return false
			}
		}
		return true
	}
	return false
}

// FieldPath is the fields a pointer goes through.
func (s *Spec) FieldPath(ptr string) ([]Field, bool) {
	fs := s.Fields
	var out []Field
	for seg := range strings.SplitSeq(strings.Trim(ptr, "/"), "/") {
		i := slices.IndexFunc(fs, func(f Field) bool { return f.Name == seg })
		if i < 0 {
			return nil, false
		}
		out = append(out, fs[i])
		fs = fs[i].Fields
	}
	return out, len(out) > 0
}

func (s *Spec) langs() []string {
	if len(s.Langs) == 0 {
		return []string{docs.DefaultBase}
	}
	return s.Langs
}

// documented says whether the spec says anything docs/ is for: a language, a title, a description, a group.
func (s *Spec) documented() bool {
	if len(s.Langs) > 0 || len(s.Title) > 0 || len(s.Doc) > 0 || len(s.Groups) > 0 {
		return true
	}
	var says func(fs []Field) bool
	says = func(fs []Field) bool {
		for _, f := range fs {
			if len(f.Title) > 0 || len(f.Description) > 0 || len(f.Examples) > 0 || len(f.I18n) > 0 || says(f.Fields) {
				return true
			}
		}
		return false
	}
	if says(s.Fields) {
		return true
	}
	for _, a := range s.Actions {
		if len(a.Description) > 0 {
			return true
		}
	}
	return false
}

// Docs are the docs/<lang>.yaml of the spec, by language: what sdk docs.Skeleton writes of a schema that says the
// spec's texts, so that the files are in the subset the SDKs read.
func (s *Spec) Docs() (map[string][]byte, error) {
	if !s.documented() {
		return nil, nil // the pack is not documented: no docs/, as it was written
	}
	langs := s.langs()
	i18n := func(t map[string]Text) map[string]any {
		out := map[string]any{}
		for l, x := range t {
			m := map[string]any{}
			if len(x.Title) > 0 {
				m["title"] = x.Title
			}
			if len(x.Description) > 0 {
				m["description"] = x.Description
			}
			out[l] = m
		}
		return out
	}
	texts := func(t map[string]string, key string) map[string]any {
		out := map[string]any{}
		for l, x := range t {
			out[l] = map[string]any{key: x}
		}
		return out
	}
	var node func(f Field, ptr string) map[string]any
	node = func(f Field, ptr string) map[string]any {
		n := map[string]any{"type": "string"}
		if len(f.Title) > 0 {
			n["title"] = f.Title
		}
		if len(f.Description) > 0 {
			n["description"] = f.Description
		}
		if len(f.Examples) > 0 {
			n["examples"] = f.Examples
		}
		if len(f.Group) > 0 {
			n[docs.KeyGroup] = f.Group
		}
		if len(f.I18n) > 0 {
			n[docs.KeyI18n] = i18n(f.I18n)
		}
		if rules := s.Rules[ptr]; len(rules) > 0 {
			var rs []any
			for _, r := range rules {
				m := map[string]any{"rule": r.Rule}
				if len(r.ID) > 0 {
					m["id"] = r.ID
				}
				if len(r.Message) > 0 {
					m["message"] = r.Message
				}
				if len(r.I18n) > 0 {
					m[docs.KeyI18n] = r.I18n
				}
				rs = append(rs, m)
			}
			n["x-pack-rules"] = rs
		}
		if len(f.Fields) > 0 {
			props := map[string]any{}
			for _, c := range f.Fields {
				props[c.Name] = node(c, ptr+"/"+c.Name)
			}
			n["type"], n["properties"] = "object", props
		}
		return n
	}
	root := node(Field{Title: s.Title, Description: s.Doc, I18n: s.I18n, Fields: s.Fields}, "")
	if len(s.Fields) == 0 {
		root["type"] = "object"
	}
	root[docs.KeyLangs] = langs
	var groups []any
	for _, g := range s.Groups {
		m := map[string]any{"id": g.ID, "title": g.Title}
		if len(g.I18n) > 0 {
			m[docs.KeyI18n] = texts(g.I18n, "title")
		}
		groups = append(groups, m)
	}
	if len(groups) > 0 {
		root[docs.KeyGroups] = groups
	}
	var diags []any
	for _, d := range s.Diagnose {
		m := map[string]any{"id": d.ID, "match": d.Match, "cause": d.Cause}
		if len(d.I18n) > 0 {
			m[docs.KeyI18n] = d.I18n
		}
		diags = append(diags, m)
	}
	if len(diags) > 0 {
		root["x-pack-diagnose"] = diags
	}
	var acts []any
	for _, a := range s.Actions {
		m := map[string]any{"name": a.Name, "command": a.Command}
		if len(a.Description) > 0 {
			m["description"] = a.Description
		}
		if len(a.I18n) > 0 {
			m[docs.KeyI18n] = texts(a.I18n, "description")
		}
		if len(a.Params) > 0 {
			props := map[string]any{}
			for _, p := range a.Params {
				pm := map[string]any{"type": p.Type}
				if len(p.Description) > 0 {
					pm["description"] = p.Description
				}
				if len(p.I18n) > 0 {
					pm[docs.KeyI18n] = texts(p.I18n, "description")
				}
				props[p.Name] = pm
			}
			m["params"] = map[string]any{"type": "object", "properties": props}
		}
		acts = append(acts, m)
	}
	if len(acts) > 0 {
		root["x-pack-actions"] = acts
	}
	raw, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	sch, err := schema.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("spec: its documentation: %w", err)
	}
	out := map[string][]byte{}
	for _, l := range langs {
		out[l] = docs.Skeleton(sch, l)
	}
	return out, nil
}

// FromPack is the spec of a generated pack (its directory: schema.json, pack.yaml, templates/): what pack author
// scaffold --from writes the source of again, in a language — the pack ported. A pack that holds another is not
// one it says yet.
func FromPack(dir string) (*Spec, error) {
	raw, err := os.ReadFile(filepath.Join(dir, format.SchemaFile))
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	s := &Spec{}
	if err := s.metaFrom(dir); err != nil {
		return nil, err
	}
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	s.Title, s.Doc = str(doc, "title"), str(doc, "description")
	s.I18n = textsOf(doc[docs.KeyI18n])
	if l, ok := doc[docs.KeyLangs].([]any); ok {
		for _, x := range l {
			s.Langs = append(s.Langs, fmt.Sprint(x))
		}
	}
	if gs, ok := doc[docs.KeyGroups].([]any); ok {
		for _, g := range gs {
			gm, _ := g.(map[string]any)
			grp := Group{ID: str(gm, "id"), Title: str(gm, "title")}
			for l, t := range textsOf(gm[docs.KeyI18n]) {
				if grp.I18n == nil {
					grp.I18n = map[string]string{}
				}
				grp.I18n[l] = t.Title
			}
			s.Groups = append(s.Groups, grp)
		}
	}
	if x, ok := doc["x-pack"]; ok {
		if err := decode(x, &s.Root); err != nil {
			return nil, fmt.Errorf("x-pack: %w", err)
		}
	}
	if x, ok := doc["x-pack-diagnose"]; ok {
		if err := decode(x, &s.Diagnose); err != nil {
			return nil, fmt.Errorf("x-pack-diagnose: %w", err)
		}
	}
	if x, ok := doc["x-pack-actions"]; ok {
		var as []packschema.Action
		if err := decode(x, &as); err != nil {
			return nil, fmt.Errorf("x-pack-actions: %w", err)
		}
		for _, a := range as {
			sa := Action{Name: a.Name, Description: a.Description, Command: a.Command, Timeout: a.Timeout, Mutating: a.Mutating, Approve: a.Approve}
			for l, t := range a.I18n {
				if sa.I18n == nil {
					sa.I18n = map[string]string{}
				}
				sa.I18n[l] = t["description"]
			}
			if a.Schema != nil {
				for _, name := range slices.Sorted(maps.Keys(a.Schema.Properties)) {
					p := a.Schema.Properties[name]
					sp := Param{Name: name, Type: p.Type, Required: slices.Contains(a.Schema.Required, name), Default: p.Default, Enum: p.Enum, Description: p.Description}
					for l, t := range p.I18n {
						if sp.I18n == nil {
							sp.I18n = map[string]string{}
						}
						sp.I18n[l] = t["description"]
					}
					sa.Params = append(sa.Params, sp)
				}
			}
			s.Actions = append(s.Actions, sa)
		}
	}
	order := map[string][]string{}
	if err := orderOf(raw, "", order); err != nil {
		return nil, err
	}
	if s.Fields, err = s.fieldsFrom(doc, "", order); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Spec) metaFrom(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, format.PackFile))
	if err != nil {
		return err
	}
	var pf struct {
		Annotations map[string]string `json:"annotations"`
		Items       []struct {
			From        string            `json:"from"`
			Annotations map[string]string `json:"annotations"`
		} `json:"items"`
	}
	if err := yaml.Unmarshal(raw, &pf); err != nil {
		return fmt.Errorf("%s: %w", format.PackFile, err)
	}
	a := maps.Clone(pf.Annotations)
	take := func(k string) string { v := a[k]; delete(a, k); return v }
	s.Name, s.Version = take("pack.name"), take("pack.version")
	const oc = "org.opencontainers.image."
	s.Description, s.Documentation, s.Source, s.URL = take(oc+"description"), take(oc+"documentation"), take(oc+"source"), take(oc+"url")
	s.License, s.Vendor, s.Authors = take(oc+"licenses"), take(oc+"vendor"), take(oc+"authors")
	take(oc + "title")
	take(oc + "version")
	if len(a) > 0 {
		s.Annotations = a
	}
	for _, it := range pf.Items {
		if it.Annotations["pack.image"] == "true" {
			s.Image = strings.TrimPrefix(it.From, "oci://")
		}
	}
	return nil
}

// orderOf is the order of the properties of each object of a schema, by its pointer: JSON says it, a map does not.
func orderOf(raw []byte, ptr string, out map[string][]string) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	var m map[string]json.RawMessage
	if err := dec.Decode(&m); err != nil {
		return err
	}
	props, ok := m["properties"]
	if !ok {
		return nil
	}
	keys, err := keysInOrder(props)
	if err != nil {
		return err
	}
	out[ptr] = keys
	var pm map[string]json.RawMessage
	if err := json.Unmarshal(props, &pm); err != nil {
		return err
	}
	for _, k := range keys {
		if err := orderOf(pm[k], ptr+"/"+k, out); err != nil {
			return err
		}
	}
	return nil
}

func keysInOrder(obj json.RawMessage) ([]string, error) {
	dec := json.NewDecoder(strings.NewReader(string(obj)))
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		keys = append(keys, t.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// known are the keywords of a node that FromPack reads; another is a pack it cannot say yet.
var known = []string{"type", "properties", "required", "additionalProperties", "items", "default", "title", "description",
	"examples", "format", "x-pack-env", "x-pack-compute", "x-pack-rules", "x-pack-suggest", "x-pack-group", "x-pack-i18n"}

func (s *Spec) fieldsFrom(node map[string]any, ptr string, order map[string][]string) ([]Field, error) {
	props, _ := node["properties"].(map[string]any)
	var req []string
	if r, ok := node["required"].([]any); ok {
		for _, x := range r {
			req = append(req, fmt.Sprint(x))
		}
	}
	var out []Field
	for _, name := range order[ptr] {
		n, _ := props[name].(map[string]any)
		p := ptr + "/" + name
		if ref, ok := n["$ref"]; ok {
			return nil, fmt.Errorf("%s: the pack holds another (%v): a spec does not say that yet — scaffold the held pack by itself", p, ref)
		}
		for k := range n {
			if !slices.Contains(known, k) {
				return nil, fmt.Errorf("%s: %s is not said by a spec yet", p, k)
			}
		}
		f := Field{Name: name, Required: slices.Contains(req, name), Default: n["default"]}
		f.Env, _ = n["x-pack-env"].(string)
		f.Title, _ = n["title"].(string)
		f.Description, _ = n["description"].(string)
		f.Group, _ = n["x-pack-group"].(string)
		if ex, ok := n["examples"].([]any); ok {
			f.Examples = ex
		}
		f.I18n = textsOf(n[docs.KeyI18n])
		t, _ := n["type"].(string)
		switch {
		case n["format"] == "password":
			f.Type = TSecret
		case t == "object" && n["properties"] != nil:
			f.Type = TObject
			var err error
			if f.Fields, err = s.fieldsFrom(n, p, order); err != nil {
				return nil, err
			}
		case t == "object":
			f.Type, f.Values = TMap, "any"
			if ap, ok := n["additionalProperties"].(map[string]any); ok {
				if vt, ok := ap["type"].(string); ok {
					f.Values = vt
				}
			}
		case t == "array":
			f.Type = TList
			if it, ok := n["items"].(map[string]any); ok {
				f.Items, _ = it["type"].(string)
			}
		default:
			f.Type = t
		}
		if c, ok := n["x-pack-compute"].(string); ok {
			if s.Compute == nil {
				s.Compute = map[string]string{}
			}
			s.Compute[p] = c
		}
		if x, ok := n["x-pack-rules"]; ok {
			var rs []packschema.Rule
			if err := decode(x, &rs); err != nil {
				return nil, fmt.Errorf("%s: x-pack-rules: %w", p, err)
			}
			if s.Rules == nil {
				s.Rules = map[string][]packschema.Rule{}
			}
			s.Rules[p] = rs
		}
		if x, ok := n["x-pack-suggest"]; ok {
			var sg packschema.Suggest
			if err := decode(x, &sg); err != nil {
				return nil, fmt.Errorf("%s: x-pack-suggest: %w", p, err)
			}
			if s.Suggest == nil {
				s.Suggest = map[string]packschema.Suggest{}
			}
			s.Suggest[p] = sg
		}
		out = append(out, f)
	}
	return out, nil
}

func textsOf(x any) map[string]Text {
	m, _ := x.(map[string]any)
	var out map[string]Text
	for l, v := range m {
		vm, _ := v.(map[string]any)
		t := Text{}
		t.Title, _ = vm["title"].(string)
		t.Description, _ = vm["description"].(string)
		if out == nil {
			out = map[string]Text{}
		}
		out[l] = t
	}
	return out
}

func decode(x any, into any) error {
	b, err := json.Marshal(x)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}
