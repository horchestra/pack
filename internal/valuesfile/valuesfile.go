// Package valuesfile writes a release's values.yaml from the pack's schema: every setting of the pack and of the
// packs it holds, in the schema's order, with its documentation (docs/, in the release's language) as comments above
// it. What the operator gave is written as it is; what they did not is a commented line — `#port: 5432` — the
// setting's default (or an example) to uncomment. A value the tool filled says where it came from (`# source: …`).
//
// The file is the operator's to edit: they set values, uncomment lines, and the next write keeps all of it. A comment
// of their own is what a write would lose, so the first line stamps the comments pack wrote; a file whose comments
// are not those is rewritten only when asked (Foreign).
package valuesfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/sdk/docs"
	"github.com/horchestra/pack/sdk/packschema"
	"github.com/horchestra/pack/sdk/schema"
)

// ---- where the values came from

// SourcesFile is the record of where the values the tool wrote came from, in the run's directory (.pack).
const SourcesFile = "values.sources.json"

// The sources of a value.
const (
	FromOperator = "operator"
	FromSuggest  = "suggest"
	FromDefault  = "default"
	FromCompute  = "compute"
	FromLLM      = "llm"
)

// Source is where a value came from: the value as it was written, so that a value the operator changed since is
// theirs.
type Source struct {
	Value any    `json:"value"`
	From  string `json:"from"`
	Why   string `json:"why,omitempty"`
}

// Sources are the sources by the value's JSON Pointer. What fills values for the operator (pack fill, its suggestions:
// horchestrapack-17) saves them; values write only reads them.
type Sources map[string]Source

// LoadSources reads dir/values.sources.json; none — no sources.
func LoadSources(dir string) (Sources, error) {
	raw, err := os.ReadFile(filepath.Join(dir, SourcesFile))
	if os.IsNotExist(err) {
		return Sources{}, nil
	}
	if err != nil {
		return nil, err
	}
	s := Sources{}
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", SourcesFile, err)
	}
	return s, nil
}

// Save writes dir/values.sources.json.
func (s Sources) Save(dir string) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, SourcesFile), append(raw, '\n'), 0o644)
}

// Of is where the value at ptr came from, when it is still the value the record has; else the operator's.
func (s Sources) Of(ptr string, v any) Source {
	r, ok := s[ptr]
	if !ok || !same(r.Value, v) {
		return Source{Value: v, From: FromOperator}
	}
	return r
}

func same(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

// ---- the language

// Lang picks the language of the comments: the environment's (lang:), else $LANG's (ru_RU.UTF-8 is ru-RU), else
// "" — the pack's base language.
func Lang(env string) string {
	if len(env) > 0 {
		return env
	}
	l := os.Getenv("LANG")
	l, _, _ = strings.Cut(l, ".")
	if len(l) == 0 || l == "C" || l == "POSIX" {
		return ""
	}
	return strings.ReplaceAll(l, "_", "-")
}

// pick is the language of a pack's texts lang matches: the language itself, its first part (ru for ru-RU), or the
// base one ("").
func pick(langs []string, lang string) string {
	if len(lang) == 0 || len(langs) == 0 {
		return ""
	}
	if slices.Contains(langs[1:], lang) {
		return lang
	}
	if short, _, ok := strings.Cut(lang, "-"); ok && slices.Contains(langs[1:], short) {
		return short
	}
	return ""
}

// ---- the file

type writer struct {
	b       *bundle.Bundle
	lang    string
	schemas map[string]*schema.Schema
	sources Sources
	out     []string
	heading string // the group whose heading was written last, at any depth
}

// Write is the values.yaml of a release: the pack's settings with values the operator's, sources where the tool
// filled them, comments in lang (Lang).
func Write(b *bundle.Bundle, values map[string]any, src Sources, lang string) ([]byte, error) {
	w := &writer{b: b, lang: lang, schemas: map[string]*schema.Schema{}, sources: src}
	root, err := w.schema(b.Root.Name)
	if err != nil {
		return nil, err
	}
	l := pick(docs.Langs(root), lang)
	title := w.text(root.Root.Raw, l, "title")
	if len(title) == 0 {
		title = b.Root.Name
	}
	w.doc(0, fmt.Sprintf("%s %s — the values of the release. A line `#key: value` is left to its default: remove the", title, b.Root.Version))
	w.doc(0, "`#` to set it. pack rewrites this file (pack values write); a comment of your own stops it, --rewrite drops it.")
	if err := w.object(b.Root.Name, root.Root, values, "", 0, true, ""); err != nil {
		return nil, err
	}
	return w.bytes(), nil
}

func (w *writer) schema(pack string) (*schema.Schema, error) {
	if s, ok := w.schemas[pack]; ok {
		return s, nil
	}
	p, ok := w.b.Packs[pack]
	if !ok {
		return nil, fmt.Errorf("no pack %q in the bundle", pack)
	}
	s, err := schema.Parse(p.SchemaRaw)
	if err != nil {
		return nil, fmt.Errorf("pack %q: %w", pack, err)
	}
	w.schemas[pack] = s
	return s, nil
}

// doc writes text as comments at indent: "# line", "#" for an empty one. A line's own leading spaces are dropped:
// "#  x" is a commented setting's continuation (isDoc), and a comment must read as one.
func (w *writer) doc(indent int, text string) {
	pad := strings.Repeat(" ", indent)
	for l := range strings.SplitSeq(text, "\n") {
		l = strings.TrimSpace(l)
		if len(l) == 0 {
			w.out = append(w.out, pad+"#")
			continue
		}
		w.out = append(w.out, pad+"# "+l)
	}
}

func (w *writer) blank() {
	if len(w.out) > 0 && len(w.out[len(w.out)-1]) > 0 {
		w.out = append(w.out, "")
	}
}

func (w *writer) add(indent int, text string) {
	w.out = append(w.out, strings.Repeat(" ", indent)+text)
}

// commented adds lines with '#' right after indent: a setting to uncomment.
func (w *writer) commented(indent int, lines []string) {
	pad := strings.Repeat(" ", indent)
	for _, l := range lines {
		w.out = append(w.out, pad+"#"+strings.TrimPrefix(l, pad))
	}
}

// text is a node's text in lang, the base language's when lang has none.
func (w *writer) text(raw map[string]any, lang, key string) string {
	if len(lang) > 0 {
		if tr, ok := raw[docs.KeyI18n].(map[string]any); ok {
			if t, ok := tr[lang].(map[string]any); ok {
				if s, ok := t[key].(string); ok && len(s) > 0 {
					return s
				}
			}
		}
	}
	s, _ := raw[key].(string)
	return s
}

// object writes the fields of node (of pack) at indent; v is the operator's object, nil when they gave none.
// group is the parent's group: its fields in the same one need no heading of it again.
func (w *writer) object(pack string, node *schema.Node, v map[string]any, ptr string, indent int, top bool, group string) error {
	s, err := w.schema(pack)
	if err != nil {
		return err
	}
	lang := pick(docs.Langs(s), w.lang)
	groups := groupTitles(s, lang, w)
	for i, k := range node.Order {
		child, held := node.Properties[k], ""
		if strings.HasPrefix(child.Ref, packschema.PackScheme) {
			url, _ := schema.SplitRef(child.Ref)
			held = strings.TrimPrefix(url, packschema.PackScheme)
			hs, err := w.schema(held)
			if err != nil {
				return err
			}
			if len(w.text(child.Raw, lang, "description")) == 0 && len(w.text(child.Raw, lang, "title")) == 0 {
				child = &schema.Node{Raw: hs.Root.Raw, Properties: hs.Root.Properties, Order: hs.Root.Order, Required: hs.Root.Required}
			} else {
				child = &schema.Node{Raw: child.Raw, Properties: hs.Root.Properties, Order: hs.Root.Order, Required: hs.Root.Required}
			}
		}
		p := ptr + "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(k)
		// a heading where the group changes, and again where a nested object wrote another one since
		if g, _ := child.Raw[docs.KeyGroup].(string); g != group || len(g) > 0 && w.heading != g {
			group = g
			if len(g) > 0 {
				w.blank()
				w.doc(indent, "--- "+groups[g]+" ---")
				w.heading = g
			}
		}
		desc := w.text(child.Raw, lang, "description")
		if len(desc) == 0 {
			desc = w.text(child.Raw, lang, "title")
		}
		if len(desc) > 0 && (i > 0 || top) {
			w.blank()
		}
		if len(desc) > 0 {
			w.doc(indent, desc)
		}
		w.hints(child, slices.Contains(node.Required, k) && len(held) == 0, indent)
		cv, has := v[k]
		owner := pack
		if len(held) > 0 {
			owner = held
		}
		if err := w.field(owner, child, k, cv, has, p, indent, group); err != nil {
			return err
		}
	}
	// what the operator wrote that the schema has not: kept, the schema's check says the rest
	var extra []string
	for k := range v {
		if _, ok := node.Properties[k]; !ok {
			extra = append(extra, k)
		}
	}
	slices.Sort(extra)
	for _, k := range extra {
		w.value(k, v[k], indent)
	}
	return nil
}

// hints are what a setting may be: examples, the values it is one of, its pattern, whether it must be given.
func (w *writer) hints(n *schema.Node, required bool, indent int) {
	if ex, ok := n.Raw["examples"].([]any); ok && len(ex) > 0 {
		w.doc(indent, "e.g. "+scalars(ex))
	}
	if len(n.Enum) > 0 {
		w.doc(indent, "one of: "+scalars(n.Enum))
	}
	if p, ok := n.Raw["pattern"].(string); ok {
		w.doc(indent, "pattern: "+p)
	}
	if len(n.Compute) > 0 {
		w.doc(indent, "filled by a formula unless set")
	}
	if required && !n.HasDefault && len(n.Compute) == 0 && len(n.Properties) == 0 {
		w.doc(indent, "required")
	}
}

func (w *writer) field(pack string, n *schema.Node, key string, v any, has bool, ptr string, indent int, group string) error {
	obj := len(n.Properties) > 0
	if has {
		if m, ok := v.(map[string]any); ok && obj {
			w.add(indent, yamlKey(key)+":")
			return w.object(pack, n, m, ptr, indent+2, false, group)
		}
		if src := w.sources.Of(ptr, v); src.From != FromOperator {
			s := "source: " + src.From
			if len(src.Why) > 0 {
				s += " — " + src.Why
			}
			w.out = append(w.out, strings.Repeat(" ", indent)+"# "+s)
		}
		w.value(key, v, indent)
		return nil
	}
	if obj {
		w.commented(indent, []string{yamlKey(key) + ":"})
		return w.object(pack, n, nil, ptr, indent+2, false, group)
	}
	w.commented(indent, template(key, n, indent))
	return nil
}

// value writes key: v as YAML at indent.
func (w *writer) value(key string, v any, indent int) {
	for _, l := range valueLines(key, v) {
		w.add(indent, l)
	}
}

// valueLines are "key: v", or "key:" and v below it, indented by two.
func valueLines(key string, v any) []string {
	b, err := yaml.Marshal(v)
	if err != nil {
		return []string{yamlKey(key) + ": null"}
	}
	body := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	_, isMap := v.(map[string]any)
	_, isList := v.([]any)
	if (isMap || isList) && body[0] != "{}" && body[0] != "[]" {
		out := []string{yamlKey(key) + ":"}
		for _, l := range body {
			out = append(out, "  "+l)
		}
		return out
	}
	out := []string{yamlKey(key) + ": " + body[0]}
	return append(out, body[1:]...)
}

// template is what a setting not given looks like set: its default, an example, a secret's reference, or nothing.
func template(key string, n *schema.Node, indent int) []string {
	pad := strings.Repeat(" ", indent)
	if n.Format == "password" {
		return []string{pad + yamlKey(key) + ":", pad + "  secretRef:", pad + `    name: ""`, pad + `    key: ""`}
	}
	var v any
	switch {
	case n.HasDefault:
		v = n.Default
	case len(n.Enum) > 0:
		v = n.Enum[0]
	default:
		if ex, ok := n.Raw["examples"].([]any); ok && len(ex) > 0 {
			v = ex[0]
		}
	}
	if v == nil {
		return []string{pad + yamlKey(key) + ":"}
	}
	var out []string
	for _, l := range valueLines(key, v) {
		out = append(out, pad+l)
	}
	return out
}

func groupTitles(s *schema.Schema, lang string, w *writer) map[string]string {
	out := map[string]string{}
	gs, _ := s.Root.Raw[docs.KeyGroups].([]any)
	for _, g := range gs {
		gm, _ := g.(map[string]any)
		id, _ := gm["id"].(string)
		out[id] = w.text(gm, lang, "title")
	}
	return out
}

func scalars(xs []any) string {
	var out []string
	for _, x := range xs {
		switch v := x.(type) {
		case string:
			out = append(out, v)
		default:
			b, _ := json.Marshal(v)
			out = append(out, string(b))
		}
	}
	return strings.Join(out, ", ")
}

func yamlKey(k string) string {
	b, err := yaml.Marshal(map[string]any{k: nil})
	if err != nil {
		return k
	}
	key, _, _ := strings.Cut(strings.TrimRight(string(b), "\n"), ":")
	return key
}

// ---- the stamp

const stampPrefix = "# pack values: sha256:"

func (w *writer) bytes() []byte {
	var body bytes.Buffer
	for _, l := range w.out {
		body.WriteString(l)
		body.WriteByte('\n')
	}
	return append([]byte(stampPrefix+stamp(body.Bytes())+"\n"), body.Bytes()...)
}

// stamp is the hash of a file's own comments, in order: the lines that are a comment and not a commented setting.
func stamp(body []byte) string {
	h := sha256.New()
	for l := range strings.SplitSeq(string(body), "\n") {
		if isDoc(l) && !strings.HasPrefix(strings.TrimLeft(l, " "), "# source: ") {
			h.Write([]byte(strings.TrimRight(l, " ") + "\n"))
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// isDoc: a comment, not a commented setting — `# text` or `#` alone, where a setting is `#key:` or `#  key:`.
func isDoc(l string) bool {
	t := strings.TrimLeft(l, " ")
	if !strings.HasPrefix(t, "#") {
		return false
	}
	rest := t[1:]
	return len(strings.TrimSpace(rest)) == 0 || (strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "  "))
}

// Foreign says what of an existing values.yaml a write would lose: a comment pack did not write — a line of
// comment whose stamp is not the file's, or a comment at the end of a line — and nothing when there is none. want
// is what Write gives for the file's values.
func Foreign(have, want []byte) []string {
	text := string(have)
	first, body, _ := strings.Cut(text, "\n")
	var out []string
	stamped, hasStamp := strings.CutPrefix(first, stampPrefix)
	if !hasStamp {
		body = text
	}
	if hasStamp && stamped != stamp([]byte(body)) || !hasStamp && stamp([]byte(body)) != stamp(nil) {
		out = append(out, "the comments are not the ones pack wrote")
	}
	gen := map[string]bool{}
	for l := range strings.SplitSeq(string(want), "\n") {
		gen[l] = true
	}
	for i, l := range strings.Split(body, "\n") {
		if !isDoc(l) && !strings.HasPrefix(strings.TrimLeft(l, " "), "#") && strings.Contains(l, " #") && !gen[l] {
			n := i + 1
			if hasStamp {
				n++
			}
			out = append(out, fmt.Sprintf("line %d: a comment at the end of the line", n))
		}
	}
	return out
}
