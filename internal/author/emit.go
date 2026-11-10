package author

import (
	"bytes"
	"fmt"
	"go/format"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/horchestra/pack/sdk/packschema"
)

// typeInfo is an object of the spec as a type of the language: its name, its fields.
type typeInfo struct {
	name   string
	fields []Field
}

// camelUpper is a json name as a type or a Go field names it: shared_buffers → SharedBuffers.
func camelUpper(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
		if r == '_' || r == '-' {
			up = true
			continue
		}
		if up {
			b.WriteRune(unicode.ToUpper(r))
			up = false
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// types are the objects of the spec, the pack's own first, each named once — a name taken is prefixed by its parent's.
func (s *Spec) types() (root typeInfo, nested []typeInfo, names map[string]string) {
	names = map[string]string{} // pointer → type name
	taken := map[string]bool{}
	root = typeInfo{name: camelUpper(s.Name), fields: s.Fields}
	taken[root.name] = true
	var walk func(parent, ptr string, fs []Field)
	walk = func(parent, ptr string, fs []Field) {
		for _, f := range fs {
			if f.Type != TObject {
				continue
			}
			p := ptr + "/" + f.Name
			n := camelUpper(f.Name)
			if taken[n] {
				n = parent + n
			}
			taken[n] = true
			names[p] = n
			walk(n, p, f.Fields)
			nested = append(nested, typeInfo{name: n, fields: f.Fields}) // the children first: Python and Java read in order
		}
	}
	walk(root.name, "", s.Fields)
	return root, nested, names
}

// leaves are the fields with a default, by their path of field names.
func defaults(fs []Field, path []string, out *[]struct {
	path []string
	f    Field
}) {
	for _, f := range fs {
		p := append(slices.Clone(path), f.Name)
		if f.Type == TObject {
			defaults(f.Fields, p, out)
			continue
		}
		if f.Default != nil {
			*out = append(*out, struct {
				path []string
				f    Field
			}{p, f})
		}
	}
}

func (s *Spec) defaults() []struct {
	path []string
	f    Field
} {
	var out []struct {
		path []string
		f    Field
	}
	defaults(s.Fields, nil, &out)
	return out
}

// segments is a pointer's field names.
func segments(ptr string) []string { return strings.Split(strings.Trim(ptr, "/"), "/") }

// ---- Go

// GoSource is the pack's source in Go: the package of the pack (a library, as examples/ are) and its generator, a
// main that imports it from importPath. A source go/format cannot read is an error of the writer: refused, not written.
func (s *Spec) GoSource(importPath string) (pack, main []byte, err error) {
	root, nested, names := s.types()
	var b bytes.Buffer
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	pkg := strings.ReplaceAll(s.Name, "-", "")
	w("// Package %s is the %s pack, scaffolded by pack author scaffold: from now on this code is the pack.\n", pkg, s.Name)
	w("package %s\n\nimport (\n\t\"github.com/horchestra/pack/sdk\"\n\t\"github.com/horchestra/pack/sdk/packschema\"\n)\n\n", pkg)
	goType := func(f Field, ptr string) string {
		scalar := map[string]string{TString: "string", TInteger: "int", TNumber: "float64", TBoolean: "bool", "any": "any"}
		switch f.Type {
		case TSecret:
			return "sdk.Secret"
		case TObject:
			return names[ptr]
		case TList:
			return "[]" + scalar[f.Items]
		case TMap:
			return "map[string]" + scalar[f.Values]
		}
		return scalar[f.Type]
	}
	emitType := func(t typeInfo, ptr string) {
		w("type %s struct {\n", t.name)
		for _, f := range t.fields {
			tag := f.Name
			if !f.Required {
				tag += ",omitempty"
			}
			more := "" // the tags after json
			if len(f.Env) > 0 {
				more = fmt.Sprintf(" env:%q", f.Env)
			}
			if c := f.goConstraints(); len(c) > 0 {
				more += fmt.Sprintf(" jsonschema:%q", c)
			}
			w("\t%s %s `json:%q%s`\n", camelUpper(f.Name), goType(f, ptr+"/"+f.Name), tag, more)
		}
		w("}\n\n")
	}
	emitType(root, "")
	ptrOf := map[string]string{}
	for p, n := range names {
		ptrOf[n] = p
	}
	for _, t := range nested {
		emitType(t, ptrOf[t.name])
	}
	goPath := func(path []string) string {
		parts := make([]string, len(path))
		for i, p := range path {
			parts[i] = camelUpper(p)
		}
		return "p." + strings.Join(parts, ".")
	}
	pointer := func(ptr string) string { return "packschema.Pointer(p, &" + goPath(segments(ptr)) + ")" }

	w("func (p *%s) SetDefaults() {\n", root.name)
	for _, d := range s.defaults() {
		w("\t%s = %s\n", goPath(d.path), goValue(d.f, d.f.Default))
	}
	w("}\n\n")
	w("func (*%s) Meta() sdk.Meta {\n\treturn sdk.Meta{\n\t\tName: %q,\n\t\tVersion: %q,\n", root.name, s.Name, s.Version)
	for _, kv := range [][2]string{{"Description", s.Description}, {"Documentation", s.Documentation}, {"Source", s.Source}, {"URL", s.URL},
		{"License", s.License}, {"Vendor", s.Vendor}, {"Authors", s.Authors}, {"Image", s.Image}} {
		if len(kv[1]) > 0 {
			w("\t\t%s: %q,\n", kv[0], kv[1])
		}
	}
	if len(s.Annotations) > 0 {
		w("\t\tAnnotations: %s,\n", goLiteral(reflect.ValueOf(s.Annotations)))
	}
	w("\t}\n}\n\n")
	w("func (*%s) PackRoot() packschema.Root {\n\treturn %s\n}\n", root.name, goLiteral(reflect.ValueOf(s.Root)))
	if len(s.Compute) > 0 {
		w("\nfunc (p *%s) PackCompute() map[string]string {\n\treturn map[string]string{\n", root.name)
		for _, ptr := range slices.Sorted(maps.Keys(s.Compute)) {
			w("\t\t%s: %q,\n", pointer(ptr), s.Compute[ptr])
		}
		w("\t}\n}\n")
	}
	if len(s.Rules) > 0 {
		w("\nfunc (p *%s) PackRules() map[string][]packschema.Rule {\n\treturn map[string][]packschema.Rule{\n", root.name)
		for _, ptr := range slices.Sorted(maps.Keys(s.Rules)) {
			rs := slices.Clone(s.Rules[ptr])
			for i := range rs {
				rs[i].I18n = nil // docs/ says it
			}
			w("\t\t%s: %s,\n", pointer(ptr), goLiteral(reflect.ValueOf(rs)))
		}
		w("\t}\n}\n")
	}
	if len(s.Suggest) > 0 {
		w("\nfunc (p *%s) PackSuggest() map[string]packschema.Suggest {\n\treturn map[string]packschema.Suggest{\n", root.name)
		for _, ptr := range slices.Sorted(maps.Keys(s.Suggest)) {
			w("\t\t%s: %s,\n", pointer(ptr), goLiteral(reflect.ValueOf(s.Suggest[ptr])))
		}
		w("\t}\n}\n")
	}
	if len(s.Diagnose) > 0 {
		w("\nfunc (p *%s) PackDiagnose() []packschema.Diagnosis {\n\treturn []packschema.Diagnosis{", root.name)
		for _, d := range s.Diagnose {
			w("{\n\t\tID: %q,\n\t\tMatch: %q,\n\t\tCause: %q,\n", d.ID, d.Match, d.Cause)
			if len(d.Fix) > 0 {
				w("\t\tFix: []packschema.Fix{\n")
				for _, f := range d.Fix {
					var parts []string
					if len(f.Patch) > 0 {
						parts = append(parts, "Patch: "+pointer(f.Patch))
					}
					for _, kv := range [][2]string{{"Expr", f.Expr}, {"Command", f.Command}, {"Why", f.Why}} {
						if len(kv[1]) > 0 {
							parts = append(parts, kv[0]+": "+strconv.Quote(kv[1]))
						}
					}
					w("\t\t\t{%s},\n", strings.Join(parts, ", "))
				}
				w("\t\t},\n")
			}
			w("\t}, ")
		}
		b.Truncate(b.Len() - 2)
		w("}\n}\n")
	}
	if len(s.Actions) > 0 {
		var as []packschema.Action
		for _, a := range s.Actions {
			pa := packschema.Action{Name: a.Name, Command: a.Command, Timeout: a.Timeout, Mutating: a.Mutating, Approve: a.Approve}
			for _, p := range a.Params {
				pa.Params = append(pa.Params, packschema.Param{Name: p.Name, Type: p.Type, Required: p.Required, Default: p.Default, Enum: p.Enum})
			}
			as = append(as, pa)
		}
		w("\nfunc (*%s) PackActions() []packschema.Action {\n\treturn %s\n}\n", root.name, goLiteral(reflect.ValueOf(as)))
	}
	if pack, err = format.Source(b.Bytes()); err != nil {
		return nil, nil, fmt.Errorf("the Go source of %s does not parse (a fault of pack author scaffold): %w", s.Name, err)
	}

	var m bytes.Buffer
	fmt.Fprintf(&m, "// Command %s generates the %s pack: go run . generate -o out.\npackage main\n\nimport (\n\t\"github.com/horchestra/pack/sdk\"\n\n\tpack %q\n)\n\nfunc main() { sdk.Run(&pack.%s{}) }\n",
		s.Name, s.Name, importPath, root.name)
	if main, err = format.Source(m.Bytes()); err != nil {
		return nil, nil, fmt.Errorf("the Go main of %s does not parse (a fault of pack author scaffold): %w", s.Name, err)
	}
	return pack, main, nil
}

// goConstraints is the jsonschema tag of a field's enum, bounds, lengths and pattern: a comma in a value escaped,
// as the tag is split on the others.
func (f Field) goConstraints() string {
	var parts []string
	esc := func(v string) string { return strings.ReplaceAll(v, ",", `\,`) }
	for _, e := range f.Enum {
		parts = append(parts, "enum="+esc(scalarText(e)))
	}
	if f.Minimum != nil {
		parts = append(parts, "minimum="+scalarText(*f.Minimum))
	}
	if f.Maximum != nil {
		parts = append(parts, "maximum="+scalarText(*f.Maximum))
	}
	if f.MinLength != nil {
		parts = append(parts, fmt.Sprintf("minLength=%d", *f.MinLength))
	}
	if f.MaxLength != nil {
		parts = append(parts, fmt.Sprintf("maxLength=%d", *f.MaxLength))
	}
	if len(f.Pattern) > 0 {
		parts = append(parts, "pattern="+esc(f.Pattern))
	}
	return strings.Join(parts, ",")
}

// scalarText is a string as it is, a number as JSON writes it.
func scalarText(v any) string {
	if n, ok := number(v); ok {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// jDouble is a double of an annotation as Java writes it: a whole number past an int's range is no int literal.
func jDouble(n float64) string {
	t := scalarText(n)
	if !strings.Contains(t, ".") && (n > math.MaxInt32 || n < math.MinInt32) {
		t += ".0"
	}
	return t
}

// goValue is a value of a field as Go writes it.
func goValue(f Field, v any) string {
	switch f.Type {
	case TList:
		l, _ := v.([]any)
		elem := map[string]string{TString: "string", TInteger: "int", TNumber: "float64", TBoolean: "bool"}[f.Items]
		var items []string
		for _, x := range l {
			items = append(items, goScalar(f.Items, x))
		}
		return "[]" + elem + "{" + strings.Join(items, ", ") + "}"
	case TMap:
		m, _ := v.(map[string]any)
		elem := map[string]string{TString: "string", TInteger: "int", TNumber: "float64", TBoolean: "bool", "any": "any"}[f.Values]
		var items []string
		for _, k := range slices.Sorted(maps.Keys(m)) {
			items = append(items, strconv.Quote(k)+": "+goScalar(f.Values, m[k]))
		}
		return "map[string]" + elem + "{" + strings.Join(items, ", ") + "}"
	}
	return goScalar(f.Type, v)
}

func goScalar(t string, v any) string {
	switch t {
	case TInteger:
		return fmt.Sprint(toInt(v))
	case TNumber:
		return strconv.FormatFloat(toFloat(v), 'g', -1, 64)
	case TBoolean:
		return fmt.Sprint(v == true)
	case TString:
		return strconv.Quote(fmt.Sprint(v))
	}
	return goAny(v)
}

func goAny(v any) string {
	switch x := v.(type) {
	case string:
		return strconv.Quote(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case nil:
		return "nil"
	}
	return fmt.Sprint(v)
}

func toInt(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	}
	return 0
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}

// goLiteral is a value of packschema as a Go literal: its fields that are not zero.
func goLiteral(v reflect.Value) string {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return "nil"
		}
		return "&" + goLiteral(v.Elem())
	case reflect.Struct:
		var parts []string
		t := v.Type()
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() || f.Tag.Get("json") == "-" && f.Name != "Params" && f.Name != "Name" && f.Name != "Required" || v.Field(i).IsZero() {
				continue
			}
			parts = append(parts, f.Name+": "+goLiteral(v.Field(i)))
		}
		return typeName(t) + block(parts)
	case reflect.Slice:
		var items []string
		for i := range v.Len() {
			items = append(items, strings.TrimPrefix(goLiteral(v.Index(i)), elemPrefix(v.Type().Elem())))
		}
		if k := v.Type().Elem().Kind(); k == reflect.Struct || k == reflect.Slice {
			return "[]" + goTypeOf(v.Type().Elem()) + block(items)
		}
		return "[]" + goTypeOf(v.Type().Elem()) + "{" + strings.Join(items, ", ") + "}"
	case reflect.Map:
		var items []string
		keys := v.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
		for _, k := range keys {
			items = append(items, strconv.Quote(k.String())+": "+strings.TrimPrefix(goLiteral(v.MapIndex(k)), elemPrefix(v.Type().Elem())))
		}
		return "map[" + goTypeOf(v.Type().Key()) + "]" + goTypeOf(v.Type().Elem()) + block(items)
	case reflect.String:
		return strconv.Quote(v.String())
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Interface:
		if v.IsNil() {
			return "nil"
		}
		return goAny(v.Elem().Interface())
	}
	return fmt.Sprint(v.Interface())
}

// block is the parts of a composite literal: on one line when they are short, one a line when they are not.
func block(parts []string) string {
	line := "{" + strings.Join(parts, ", ") + "}"
	if len(line) <= 80 && !strings.Contains(line, "\n") {
		return line
	}
	return "{\n" + strings.Join(parts, ",\n") + ",\n}"
}

// elemPrefix is what a composite literal of an element may leave out: its type.
func elemPrefix(t reflect.Type) string {
	if t.Kind() == reflect.Struct {
		return typeName(t)
	}
	if t.Kind() == reflect.Slice {
		return "[]" + goTypeOf(t.Elem())
	}
	return ""
}

func typeName(t reflect.Type) string {
	if strings.HasSuffix(t.PkgPath(), "sdk/packschema") {
		return "packschema." + t.Name()
	}
	return t.String()
}

func goTypeOf(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Struct:
		return typeName(t)
	case reflect.Pointer:
		return "*" + goTypeOf(t.Elem())
	case reflect.Slice:
		return "[]" + goTypeOf(t.Elem())
	case reflect.Map:
		return "map[" + goTypeOf(t.Key()) + "]" + goTypeOf(t.Elem())
	case reflect.Interface:
		return "any"
	}
	if strings.HasSuffix(t.PkgPath(), "sdk/packschema") {
		return "packschema." + t.Name()
	}
	return t.Name()
}

// ---- Python

// snake is a Go field name as Python's dataclass says it: MountPath → mount_path, TCP → tcp, From → from_.
func snake(s string) string {
	if s == "From" {
		return "from_"
	}
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		if unicode.IsUpper(r) {
			if i > 0 && (unicode.IsLower(rs[i-1]) || i+1 < len(rs) && unicode.IsLower(rs[i+1]) && unicode.IsUpper(rs[i-1])) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// PythonSource is the pack's source in Python: its dataclasses, the pack's methods, its main.
func (s *Spec) PythonSource() []byte {
	root, nested, names := s.types()
	var b bytes.Buffer
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("\"\"\"The %s pack, scaffolded by pack author scaffold: from now on this code is the pack.\"\"\"\n\n", s.Name)
	w("import sys\nfrom dataclasses import dataclass\nfrom typing import Any\n\n")
	w("from packsdk import (Action, Diagnosis, File, Fix, Hook, Meta, OSInstall, Pack, Param, Readiness, Root, Rollout, Rule, Secret, Step,\n")
	w("                     Suggest, Upgrade, Volume, When, field, main, pointer)\n\n")
	ptrOf := map[string]string{}
	for p, n := range names {
		ptrOf[n] = p
	}
	pyType := func(f Field, ptr string) (string, string) {
		scalar := map[string][2]string{TString: {"str", `""`}, TInteger: {"int", "0"}, TNumber: {"float", "0.0"}, TBoolean: {"bool", "False"}, "any": {"Any", "None"}}
		switch f.Type {
		case TSecret:
			return "Secret", "factory=Secret"
		case TObject:
			return names[ptr], "factory=" + names[ptr]
		case TList:
			return "list[" + scalar[f.Items][0] + "]", "factory=list"
		case TMap:
			return "dict[str, " + scalar[f.Values][0] + "]", "factory=dict"
		}
		return scalar[f.Type][0], scalar[f.Type][1]
	}
	emitType := func(t typeInfo, ptr, base string) {
		w("\n@dataclass\nclass %s%s:\n", t.name, base)
		for _, f := range t.fields {
			typ, def := pyType(f, ptr+"/"+f.Name)
			args := []string{def}
			if pyAttr(f.Name) != f.Name {
				args = append(args, fmt.Sprintf("json=%q", f.Name))
			}
			if len(f.Env) > 0 {
				args = append(args, fmt.Sprintf("env=%q", f.Env))
			}
			if !f.Required {
				args = append(args, "omitempty=True")
			}
			if len(f.Enum) > 0 {
				items := make([]string, len(f.Enum))
				for i, e := range f.Enum {
					items[i] = pyValue(e)
				}
				args = append(args, "enum=["+strings.Join(items, ", ")+"]")
			}
			if len(f.Pattern) > 0 {
				args = append(args, "pattern="+strconv.Quote(f.Pattern))
			}
			for _, b := range []struct {
				arg string
				v   *float64
			}{{"minimum", f.Minimum}, {"maximum", f.Maximum}} {
				if b.v != nil {
					args = append(args, b.arg+"="+scalarText(*b.v))
				}
			}
			for _, l := range []struct {
				arg string
				v   *int
			}{{"min_length", f.MinLength}, {"max_length", f.MaxLength}} {
				if l.v != nil {
					args = append(args, fmt.Sprintf("%s=%d", l.arg, *l.v))
				}
			}
			w("    %s: %s = field(%s)\n", pyAttr(f.Name), typ, strings.Join(args, ", "))
		}
	}
	for _, t := range nested {
		emitType(t, ptrOf[t.name], "")
		w("\n")
	}
	emitType(root, "", "(Pack)")
	w("\n    def set_defaults(self):\n")
	ds := s.defaults()
	for _, d := range ds {
		w("        self.%s = %s\n", pyPath(d.path), pyValue(d.f.Default))
	}
	if len(ds) == 0 {
		w("        pass\n")
	}
	var meta []string
	for _, kv := range [][2]string{{"name", s.Name}, {"version", s.Version}, {"description", s.Description}, {"documentation", s.Documentation},
		{"source", s.Source}, {"url", s.URL}, {"license", s.License}, {"vendor", s.Vendor}, {"authors", s.Authors}, {"image", s.Image}} {
		if len(kv[1]) > 0 {
			meta = append(meta, fmt.Sprintf("%s=%q", kv[0], kv[1]))
		}
	}
	if len(s.Annotations) > 0 {
		meta = append(meta, "annotations="+pyLiteral(reflect.ValueOf(s.Annotations)))
	}
	w("\n    def meta(self):\n        return Meta(%s)\n", strings.Join(meta, ", "))
	w("\n    def pack_root(self):\n        return %s\n", pyLiteral(reflect.ValueOf(s.Root)))
	pointer := func(ptr string) string { return "pointer(self, lambda o: o." + pyPath(segments(ptr)) + ")" }
	if len(s.Compute) > 0 {
		w("\n    def pack_compute(self):\n        return {\n")
		for _, ptr := range slices.Sorted(maps.Keys(s.Compute)) {
			w("            %s: %q,\n", pointer(ptr), s.Compute[ptr])
		}
		w("        }\n")
	}
	if len(s.Rules) > 0 {
		w("\n    def pack_rules(self):\n        return {\n")
		for _, ptr := range slices.Sorted(maps.Keys(s.Rules)) {
			rs := slices.Clone(s.Rules[ptr])
			for i := range rs {
				rs[i].I18n = nil
			}
			w("            %s: %s,\n", pointer(ptr), pyLiteral(reflect.ValueOf(rs)))
		}
		w("        }\n")
	}
	if len(s.Suggest) > 0 {
		w("\n    def pack_suggest(self):\n        return {\n")
		for _, ptr := range slices.Sorted(maps.Keys(s.Suggest)) {
			w("            %s: %s,\n", pointer(ptr), pyLiteral(reflect.ValueOf(s.Suggest[ptr])))
		}
		w("        }\n")
	}
	if len(s.Diagnose) > 0 {
		w("\n    def pack_diagnose(self):\n        return [\n")
		for _, d := range s.Diagnose {
			var fixes []string
			for _, f := range d.Fix {
				var parts []string
				if len(f.Patch) > 0 {
					parts = append(parts, "patch="+pointer(f.Patch))
				}
				for _, kv := range [][2]string{{"expr", f.Expr}, {"command", f.Command}, {"why", f.Why}} {
					if len(kv[1]) > 0 {
						parts = append(parts, fmt.Sprintf("%s=%q", kv[0], kv[1]))
					}
				}
				fixes = append(fixes, "Fix("+strings.Join(parts, ", ")+")")
			}
			fix := ""
			if len(fixes) > 0 {
				fix = ", fix=[" + strings.Join(fixes, ", ") + "]"
			}
			w("            Diagnosis(id=%q, match=%q, cause=%q%s),\n", d.ID, d.Match, d.Cause, fix)
		}
		w("        ]\n")
	}
	if len(s.Actions) > 0 {
		w("\n    def pack_actions(self):\n        return [\n")
		for _, a := range s.Actions {
			parts := []string{fmt.Sprintf("name=%q", a.Name), "command=" + pyLiteral(reflect.ValueOf(a.Command))}
			if len(a.Timeout) > 0 {
				parts = append(parts, fmt.Sprintf("timeout=%q", a.Timeout))
			}
			if a.Mutating {
				parts = append(parts, "mutating=True")
			}
			if a.Approve {
				parts = append(parts, "approve=True")
			}
			if len(a.Params) > 0 {
				var ps []string
				for _, p := range a.Params {
					pp := []string{fmt.Sprintf("name=%q", p.Name), fmt.Sprintf("type=%q", p.Type)}
					if p.Required {
						pp = append(pp, "required=True")
					}
					if p.Default != nil {
						pp = append(pp, "default="+pyValue(p.Default))
					}
					if len(p.Enum) > 0 {
						pp = append(pp, "enum="+pyValue(p.Enum))
					}
					ps = append(ps, "Param("+strings.Join(pp, ", ")+")")
				}
				parts = append(parts, "params=["+strings.Join(ps, ", ")+"]")
			}
			w("            Action(%s),\n", strings.Join(parts, ", "))
		}
		w("        ]\n")
	}
	w("\n\nif __name__ == \"__main__\":\n    sys.exit(main(%s()))\n", root.name)
	return b.Bytes()
}

// pyAttr is a json name as a Python attribute: log-driver → log_driver.
func pyAttr(name string) string { return strings.ReplaceAll(name, "-", "_") }

func pyPath(path []string) string {
	out := make([]string, len(path))
	for i, p := range path {
		out[i] = pyAttr(p)
	}
	return strings.Join(out, ".")
}

// pyValue is a value of JSON as Python writes it.
func pyValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return strconv.Quote(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case []any:
		var items []string
		for _, i := range x {
			items = append(items, pyValue(i))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case map[string]any:
		var items []string
		for _, k := range slices.Sorted(maps.Keys(x)) {
			items = append(items, strconv.Quote(k)+": "+pyValue(x[k]))
		}
		return "{" + strings.Join(items, ", ") + "}"
	}
	return fmt.Sprint(v)
}

// pyLiteral is a value of packschema as the constructors of packsdk write it.
func pyLiteral(v reflect.Value) string {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return "None"
		}
		return pyLiteral(v.Elem())
	case reflect.Struct:
		var parts []string
		t := v.Type()
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() || f.Tag.Get("json") == "-" || v.Field(i).IsZero() {
				continue
			}
			parts = append(parts, snake(f.Name)+"="+pyLiteral(v.Field(i)))
		}
		return t.Name() + "(" + strings.Join(parts, ", ") + ")"
	case reflect.Slice:
		var items []string
		for i := range v.Len() {
			items = append(items, pyLiteral(v.Index(i)))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case reflect.Map:
		keys := v.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
		var items []string
		for _, k := range keys {
			items = append(items, strconv.Quote(k.String())+": "+pyLiteral(v.MapIndex(k)))
		}
		return "{" + strings.Join(items, ", ") + "}"
	case reflect.String:
		return strconv.Quote(v.String())
	case reflect.Bool:
		if v.Bool() {
			return "True"
		}
		return "False"
	case reflect.Int, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Interface:
		if v.IsNil() {
			return "None"
		}
		return pyValue(v.Elem().Interface())
	}
	return fmt.Sprint(v.Interface())
}

// ---- Java

// JavaSource is the pack's source in Java: one class, its settings as nested classes, its main.
func (s *Spec) JavaSource() []byte {
	root, nested, names := s.types()
	var b bytes.Buffer
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	for _, imp := range []string{"Action", "Diagnosis", "Meta", "Pack", "PackSdk", "Paths", "Root", "Rule", "Secret", "Setting", "Suggest"} {
		w("import io.horchestra.pack.sdk.%s;\n", imp)
	}
	w("\nimport java.util.List;\nimport java.util.Map;\nimport java.util.TreeMap;\n\n")
	w("/** The %s pack, scaffolded by pack author scaffold: from now on this code is the pack. */\npublic class %s extends Pack {\n", s.Name, root.name)
	ptrOf := map[string]string{}
	for p, n := range names {
		ptrOf[n] = p
	}
	jType := func(f Field, ptr string) (string, string) {
		scalar := map[string][3]string{TString: {"String", `""`, "String"}, TInteger: {"int", "0", "Integer"}, TNumber: {"double", "0", "Double"},
			TBoolean: {"boolean", "false", "Boolean"}, "any": {"Object", "null", "Object"}}
		switch f.Type {
		case TSecret:
			return "Secret", "new Secret()"
		case TObject:
			return names[ptr], "new " + names[ptr] + "()"
		case TList:
			return "List<" + scalar[f.Items][2] + ">", "List.of()"
		case TMap:
			return "Map<String, " + scalar[f.Values][2] + ">", "new TreeMap<>()"
		}
		return scalar[f.Type][0], scalar[f.Type][1]
	}
	emitFields := func(fs []Field, ptr, indent string) {
		for _, f := range fs {
			var attrs []string
			if camel(f.Name) != f.Name {
				attrs = append(attrs, fmt.Sprintf("json = %q", f.Name))
			}
			if len(f.Env) > 0 {
				attrs = append(attrs, fmt.Sprintf("env = %q", f.Env))
			}
			if !f.Required {
				attrs = append(attrs, "omitempty = true")
			}
			if len(f.Enum) > 0 {
				items := make([]string, len(f.Enum))
				for i, e := range f.Enum {
					items[i] = strconv.Quote(scalarText(e))
				}
				attrs = append(attrs, "enumOf = {"+strings.Join(items, ", ")+"}")
			}
			if len(f.Pattern) > 0 {
				attrs = append(attrs, "pattern = "+strconv.Quote(f.Pattern))
			}
			if f.Minimum != nil {
				attrs = append(attrs, "minimum = "+jDouble(*f.Minimum))
			}
			if f.Maximum != nil {
				attrs = append(attrs, "maximum = "+jDouble(*f.Maximum))
			}
			if f.MinLength != nil {
				attrs = append(attrs, fmt.Sprintf("minLength = %d", *f.MinLength))
			}
			if f.MaxLength != nil {
				attrs = append(attrs, fmt.Sprintf("maxLength = %d", *f.MaxLength))
			}
			if len(attrs) > 0 {
				w("%s@Setting(%s)\n", indent, strings.Join(attrs, ", "))
			}
			typ, def := jType(f, ptr+"/"+f.Name)
			w("%spublic %s %s = %s;\n", indent, typ, camel(f.Name), def)
		}
	}
	for _, t := range nested {
		w("    public static class %s {\n", t.name)
		emitFields(t.fields, ptrOf[t.name], "        ")
		w("    }\n\n")
	}
	emitFields(root.fields, "", "    ")
	jPath := func(path []string) string {
		parts := make([]string, len(path))
		for i, p := range path {
			parts[i] = camel(p)
		}
		return strings.Join(parts, ".")
	}
	pointer := func(ptr string) string {
		segs := segments(ptr)
		qs := make([]string, len(segs))
		for i, sg := range segs {
			qs[i] = strconv.Quote(camel(sg))
		}
		return "Paths.pointer(" + root.name + ".class, " + strings.Join(qs, ", ") + ")"
	}
	w("\n    @Override\n    public void setDefaults() {\n")
	for _, d := range s.defaults() {
		w("        %s = %s;\n", jPath(d.path), javaValue(d.f, d.f.Default))
	}
	w("    }\n")
	w("\n    @Override\n    public Meta meta() {\n        return new Meta(%q, %q)", s.Name, s.Version)
	for _, kv := range [][2]string{{"description", s.Description}, {"documentation", s.Documentation}, {"source", s.Source}, {"url", s.URL},
		{"license", s.License}, {"vendor", s.Vendor}, {"authors", s.Authors}, {"image", s.Image}} {
		if len(kv[1]) > 0 {
			w(".%s(%q)", kv[0], kv[1])
		}
	}
	for _, k := range slices.Sorted(maps.Keys(s.Annotations)) {
		w(".annotation(%q, %q)", k, s.Annotations[k])
	}
	w("\n                .dir(System.getProperty(\"pack.dir\", \".\"));\n    }\n")
	w("\n    @Override\n    public Root packRoot() {\n        return new Root()%s;\n    }\n", javaRoot(s.Root))
	if len(s.Compute) > 0 {
		var es []string
		for _, ptr := range slices.Sorted(maps.Keys(s.Compute)) {
			es = append(es, fmt.Sprintf("Map.entry(%s, %q)", pointer(ptr), s.Compute[ptr]))
		}
		w("\n    @Override\n    public Map<String, String> packCompute() {\n        return Map.ofEntries(%s);\n    }\n", strings.Join(es, ", "))
	}
	if len(s.Rules) > 0 {
		var es []string
		for _, ptr := range slices.Sorted(maps.Keys(s.Rules)) {
			var rs []string
			for _, r := range s.Rules[ptr] {
				var x strings.Builder
				x.WriteString(fmt.Sprintf("new Rule(%q)", r.Rule))
				if len(r.Message) > 0 {
					x.WriteString(fmt.Sprintf(".message(%q)", r.Message))
				}
				if len(r.ID) > 0 {
					x.WriteString(fmt.Sprintf(".id(%q)", r.ID))
				}
				for _, ph := range r.Phases {
					x.WriteString(fmt.Sprintf(".phase(%q)", string(ph)))
				}
				rs = append(rs, x.String())
			}
			es = append(es, fmt.Sprintf("Map.entry(%s, List.of(%s))", pointer(ptr), strings.Join(rs, ", ")))
		}
		w("\n    @Override\n    public Map<String, List<Rule>> packRules() {\n        return Map.ofEntries(%s);\n    }\n", strings.Join(es, ", "))
	}
	if len(s.Suggest) > 0 {
		var es []string
		for _, ptr := range slices.Sorted(maps.Keys(s.Suggest)) {
			sg := s.Suggest[ptr]
			es = append(es, fmt.Sprintf("Map.entry(%s, new Suggest(%q, %q))", pointer(ptr), sg.Expr, sg.Why))
		}
		w("\n    @Override\n    public Map<String, Suggest> packSuggest() {\n        return Map.ofEntries(%s);\n    }\n", strings.Join(es, ", "))
	}
	if len(s.Diagnose) > 0 {
		var ds []string
		for _, d := range s.Diagnose {
			var x strings.Builder
			x.WriteString(fmt.Sprintf("new Diagnosis(%q, %q, %q)", d.ID, d.Match, d.Cause))
			for _, f := range d.Fix {
				if len(f.Patch) > 0 {
					x.WriteString(fmt.Sprintf(".patch(%s, %q, %q)", pointer(f.Patch), f.Expr, f.Why))
				} else {
					x.WriteString(fmt.Sprintf(".command(%q, %q)", f.Command, f.Why))
				}
			}
			ds = append(ds, x.String())
		}
		w("\n    @Override\n    public List<Diagnosis> packDiagnose() {\n        return List.of(%s);\n    }\n", strings.Join(ds, ",\n                "))
	}
	if len(s.Actions) > 0 {
		var as []string
		for _, a := range s.Actions {
			var x strings.Builder
			x.WriteString("new Action(" + strconv.Quote(a.Name))
			for _, c := range a.Command {
				x.WriteString(", " + strconv.Quote(c))
			}
			x.WriteString(")")
			if len(a.Timeout) > 0 {
				x.WriteString(fmt.Sprintf(".timeout(%q)", a.Timeout))
			}
			if a.Mutating {
				x.WriteString(".mutating()")
			}
			if a.Approve {
				x.WriteString(".approve()")
			}
			for _, p := range a.Params {
				x.WriteString(fmt.Sprintf("\n                        .param(Action.Param.of(%q, %q, %v)", p.Name, p.Type, p.Required))
				if p.Default != nil {
					x.WriteString(".withDefault(" + javaAny(p.Default) + ")")
				}
				if len(p.Enum) > 0 {
					var es []string
					for _, e := range p.Enum {
						es = append(es, javaAny(e))
					}
					x.WriteString(".withEnum(" + strings.Join(es, ", ") + ")")
				}
				x.WriteString(")")
			}
			as = append(as, x.String())
		}
		w("\n    @Override\n    public List<Action> packActions() {\n        return List.of(\n                %s);\n    }\n", strings.Join(as, ",\n                "))
	}
	w("\n    public static void main(String[] args) {\n        System.exit(PackSdk.main(args, new %s()));\n    }\n}\n", root.name)
	return b.Bytes()
}

func javaValue(f Field, v any) string {
	switch f.Type {
	case TList:
		l, _ := v.([]any)
		var items []string
		for _, x := range l {
			items = append(items, javaScalar(f.Items, x))
		}
		return "List.of(" + strings.Join(items, ", ") + ")"
	case TMap:
		m, _ := v.(map[string]any)
		var items []string
		for _, k := range slices.Sorted(maps.Keys(m)) {
			items = append(items, strconv.Quote(k)+", "+javaScalar(f.Values, m[k]))
		}
		return "new TreeMap<>(Map.of(" + strings.Join(items, ", ") + "))"
	}
	return javaScalar(f.Type, v)
}

func javaScalar(t string, v any) string {
	switch t {
	case TInteger:
		return fmt.Sprint(toInt(v))
	case TNumber:
		return strconv.FormatFloat(toFloat(v), 'g', -1, 64)
	case TBoolean:
		return fmt.Sprint(v == true)
	case TString:
		return strconv.Quote(fmt.Sprint(v))
	}
	return javaAny(v)
}

func javaAny(v any) string {
	switch x := v.(type) {
	case string:
		return strconv.Quote(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10) + "L"
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case nil:
		return "null"
	}
	return fmt.Sprint(v)
}

func javaStrings(l []string) string {
	qs := make([]string, len(l))
	for i, s := range l {
		qs[i] = strconv.Quote(s)
	}
	return strings.Join(qs, ", ")
}

func javaFile(f packschema.File) string {
	var x strings.Builder
	x.WriteString(fmt.Sprintf("new Root.File(%q)", f.Path))
	for _, kv := range [][2]string{{"source", f.Source}, {"template", f.Template}, {"url", f.URL}, {"values", f.Values}, {"format", f.Format},
		{"parseAs", f.ParseAs}, {"mode", f.Mode}} {
		if len(kv[1]) > 0 {
			x.WriteString(fmt.Sprintf(".%s(%q)", kv[0], kv[1]))
		}
	}
	if f.PerInstance {
		x.WriteString(".perInstance()")
	}
	return x.String()
}

// javaRoot is the calls of Root's builder that make r.
func javaRoot(r packschema.Root) string {
	var b strings.Builder
	call := func(format string, a ...any) { b.WriteString("\n                ." + fmt.Sprintf(format, a...)) }
	for _, os := range slices.Sorted(maps.Keys(r.Packages)) {
		p := r.Packages[os]
		var x strings.Builder
		x.WriteString("new Root.OSInstall()")
		if len(p.Prepare) > 0 {
			x.WriteString(".prepare(" + javaStrings(p.Prepare) + ")")
		}
		for _, f := range p.Repo {
			x.WriteString(".repo(" + javaFile(f) + ")")
		}
		x.WriteString(".names(" + javaStrings(p.Names) + ")")
		for _, c := range p.Commands {
			x.WriteString(".command(" + javaStrings(c) + ")")
		}
		for _, f := range p.Content {
			x.WriteString(".content(" + javaFile(f) + ")")
		}
		if len(p.Services) > 0 {
			x.WriteString(".services(" + javaStrings(p.Services) + ")")
		}
		call("packages(%q, %s)", os, x.String())
	}
	for _, f := range r.Content {
		call("content(%s)", javaFile(f))
	}
	for _, name := range slices.Sorted(maps.Keys(r.Ports)) {
		call("port(%q, %d)", name, r.Ports[name])
	}
	for _, name := range slices.Sorted(maps.Keys(r.Data)) {
		v := r.Data[name]
		x := fmt.Sprintf("new Root.Volume(%q)", v.MountPath)
		if len(v.Size) > 0 {
			x += fmt.Sprintf(".size(%q)", v.Size)
		}
		call("data(%q, %s)", name, x)
	}
	for _, k := range slices.Sorted(maps.Keys(r.Minimum)) {
		call("minimum(%q, %q)", k, r.Minimum[k])
	}
	if rd := r.Readiness; rd != nil {
		switch {
		case len(rd.TCP) > 0:
			call("readiness(Root.Readiness.tcp(%q))", rd.TCP)
		case len(rd.HTTP) > 0:
			call("readiness(Root.Readiness.http(%q))", rd.HTTP)
		case len(rd.Exec) > 0:
			call("readiness(Root.Readiness.exec(%s))", javaStrings(rd.Exec))
		}
	}
	if r.Upgrade != nil {
		call("upgrade(new Root.Upgrade(%q))", r.Upgrade.From)
	}
	for _, h := range r.Hooks {
		x := fmt.Sprintf("new Root.Hook(%q, %q, %s)", h.Name, string(h.Phase), javaStrings(h.Command))
		if h.When != nil {
			wh := "new Root.When()"
			if len(h.When.From) > 0 {
				wh += fmt.Sprintf(".from(%q)", h.When.From)
			}
			if len(h.When.To) > 0 {
				wh += fmt.Sprintf(".to(%q)", h.When.To)
			}
			x += ".when(" + wh + ")"
		}
		if len(h.Image) > 0 {
			x += fmt.Sprintf(".image(%q)", h.Image)
		}
		if h.Exclusive {
			x += ".exclusive()"
		}
		if len(h.Timeout) > 0 {
			x += fmt.Sprintf(".timeout(%q)", h.Timeout)
		}
		if h.Idempotent {
			x += ".idempotent()"
		}
		if c := h.Compensate; c != nil {
			st := "new Root.Step(" + javaStrings(c.Command) + ")"
			if len(c.Image) > 0 {
				st += fmt.Sprintf(".image(%q)", c.Image)
			}
			x += ".compensate(" + st + ")"
		}
		call("hook(%s)", x)
	}
	if len(r.Command) > 0 {
		call("command(%s)", javaStrings(r.Command))
	}
	if len(r.RestartPolicy) > 0 {
		call("restartPolicy(%q)", r.RestartPolicy)
	}
	for _, sp := range r.Savepoints {
		call("savepoint(%q)", sp)
	}
	if r.Rollout != nil {
		call("rollout(new Root.Rollout(%d))", r.Rollout.MaxUnavailable)
	}
	return b.String()
}
