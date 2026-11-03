package docs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/horchestra/pack/sdk/schema"
)

// The checks and the skeleton read a schema.json as written, whichever SDK wrote it.

// Gap is something of the pack that is not documented: a warning, not an error.
type Gap struct {
	Path    string
	Message string
}

func (g Gap) String() string { return g.Path + ": " + g.Message }

// Langs are the languages of a written schema, the base first; none when the pack has no docs/.
func Langs(s *schema.Schema) []string {
	l, _ := s.Root.Raw[KeyLangs].([]any)
	var out []string
	for _, x := range l {
		if t, ok := x.(string); ok {
			out = append(out, t)
		}
	}
	return out
}

// Gaps lists what docs/ does not say: a field without a description in the base language, a text of the base
// without its translation, a rule whose message cannot be translated for want of an id.
func Gaps(s *schema.Schema) []Gap {
	langs := Langs(s)
	if len(langs) == 0 {
		return []Gap{{"/", fmt.Sprintf("not documented: no %s/<lang>.yaml next to the pack's source", Dir)}}
	}
	base, others := langs[0], langs[1:]
	var out []Gap
	add := func(ptr, format string, args ...any) { out = append(out, Gap{ptr, fmt.Sprintf(format, args...)}) }
	WalkNodes(s.Root, func(ptr string, n *schema.Node) {
		title, _ := n.Raw["title"].(string)
		desc, _ := n.Raw["description"].(string)
		if len(desc) == 0 {
			add(ptr, "no description in %s", base)
		}
		tr := translations(n.Raw[KeyI18n])
		for _, l := range others {
			if len(title) > 0 && len(tr[l]["title"]) == 0 {
				add(ptr, "no title in %s", l)
			}
			if len(desc) > 0 && len(tr[l]["description"]) == 0 {
				add(ptr, "no description in %s", l)
			}
		}
		rules, _ := n.Raw["x-pack-rules"].([]any)
		for i, r := range rules {
			rm, _ := r.(map[string]any)
			id, _ := rm["id"].(string)
			msg, _ := rm["message"].(string)
			if len(msg) == 0 || len(others) == 0 {
				continue
			}
			if len(id) == 0 {
				add(ptr, "rule %d has no id: its message cannot be translated", i)
				continue
			}
			tr := translations(rm[KeyI18n])
			for _, l := range others {
				if len(tr[l]["message"]) == 0 {
					add(ptr, "rule %s: no message in %s", id, l)
				}
			}
		}
	})
	groups, _ := s.Root.Raw[KeyGroups].([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		id, _ := gm["id"].(string)
		tr := translations(gm[KeyI18n])
		for _, l := range others {
			if len(tr[l]["title"]) == 0 {
				add("/", "group %s: no title in %s", id, l)
			}
		}
	}
	return out
}

// WalkNodes visits the pack and its fields like Walk, on a written schema.
func WalkNodes(root *schema.Node, f func(ptr string, n *schema.Node)) {
	var walk func(ptr string, n *schema.Node)
	walk = func(ptr string, n *schema.Node) {
		f(ptr, n)
		if len(n.Ref) > 0 {
			return
		}
		prefix := strings.TrimSuffix(ptr, "/")
		for _, k := range n.Order {
			walk(prefix+"/"+strings.NewReplacer("~", "~0", "/", "~1").Replace(k), n.Properties[k])
		}
	}
	walk("/", root)
}

func translations(v any) map[string]map[string]string {
	out := map[string]map[string]string{}
	m, _ := v.(map[string]any)
	for lang, t := range m {
		tm, _ := t.(map[string]any)
		out[lang] = map[string]string{}
		for k, x := range tm {
			if s, ok := x.(string); ok {
				out[lang][k] = s
			}
		}
	}
	return out
}

// Skeleton is docs/<lang>.yaml for a written schema: every field, group and rule of the pack, with what the schema
// already says in lang and "" where it says nothing; in a translation, the base language's text is a comment above
// each. The file to start a language with, or to see what a language lacks; comments of an existing file are not
// kept.
func Skeleton(s *schema.Schema, lang string) []byte {
	langs := Langs(s)
	base := lang
	if len(langs) > 0 {
		base = langs[0]
	}
	isBase := lang == base
	var b bytes.Buffer
	if isBase && base != DefaultBase {
		b.WriteString("base: true\n")
	}
	text := func(raw map[string]any, key string) (string, string) {
		bt, _ := raw[key].(string)
		if isBase {
			return bt, ""
		}
		return translations(raw[KeyI18n])[lang][key], bt
	}
	WalkNodes(s.Root, func(ptr string, n *schema.Node) {
		var e bytes.Buffer
		for _, key := range []string{"title", "description"} {
			t, hint := text(n.Raw, key)
			if !isBase && len(hint) == 0 && len(t) == 0 {
				continue // nothing to translate
			}
			if len(hint) > 0 {
				writeComment(&e, base+": "+hint, 2)
			}
			writeKey(&e, key, 2)
			writeScalar(&e, t, 2)
		}
		if isBase {
			if ex, ok := n.Raw["examples"].([]any); ok && len(ex) > 0 {
				writeKey(&e, "examples", 2)
				e.WriteByte('\n')
				for _, x := range ex {
					e.WriteString("    -")
					writeScalar(&e, scalarText(x), 4)
				}
			}
			if g, ok := n.Raw[KeyGroup].(string); ok {
				writeKey(&e, "group", 2)
				writeScalar(&e, g, 2)
			}
		}
		if e.Len() == 0 {
			return
		}
		writeKey(&b, ptr, 0)
		b.WriteByte('\n')
		b.Write(e.Bytes())
	})
	if groups, _ := s.Root.Raw[KeyGroups].([]any); len(groups) > 0 {
		b.WriteString("groups:\n")
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			id, _ := gm["id"].(string)
			t, hint := text(gm, "title")
			if len(hint) > 0 {
				writeComment(&b, base+": "+hint, 2)
			}
			writeKey(&b, id, 2)
			writeScalar(&b, t, 2)
		}
	}
	var ids []string
	msgs := map[string]map[string]any{}
	WalkNodes(s.Root, func(_ string, n *schema.Node) {
		rules, _ := n.Raw["x-pack-rules"].([]any)
		for _, r := range rules {
			rm, _ := r.(map[string]any)
			if id, _ := rm["id"].(string); len(id) > 0 && !slices.Contains(ids, id) {
				ids = append(ids, id)
				msgs[id] = rm
			}
		}
	})
	if len(ids) > 0 {
		b.WriteString("rules:\n")
		for _, id := range ids {
			t, hint := text(msgs[id], "message")
			if len(hint) > 0 {
				writeComment(&b, base+": "+hint, 2)
			}
			writeKey(&b, id, 2)
			writeScalar(&b, t, 2)
		}
	}
	return b.Bytes()
}

func scalarText(x any) string {
	switch v := x.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
