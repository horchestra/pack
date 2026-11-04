package bundle

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/horchestra/pack/sdk/docs"
	"github.com/horchestra/pack/sdk/packschema"
)

// ValuesSchema is the schema of the whole composition as the operator sees it in values.yaml: the root pack's,
// with the packs it holds inlined at their fields, every x-pack-* kept (groups, compute, suggest, rules, i18n),
// the titles and descriptions in lang — empty: the pack's base language. A form (pack-ui) renders it.
func (b *Bundle) ValuesSchema(lang string) ([]byte, error) {
	raw, err := b.inline(b.Root)
	if err != nil {
		return nil, err
	}
	base := ""
	if langs, ok := raw[docs.KeyLangs].([]any); ok && len(langs) > 0 {
		base, _ = langs[0].(string)
	}
	if len(lang) > 0 && lang != base {
		translate(raw, lang)
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// inline is the pack's raw schema with the packs it holds resolved at their fields: a {"$ref": "pack://<name>#"}
// property becomes the held pack's schema, inlined the same way, the field's own keys (title, default, …) over it.
func (b *Bundle) inline(p *Pack) (map[string]any, error) {
	var raw map[string]any
	if err := json.Unmarshal(p.SchemaRaw, &raw); err != nil {
		return nil, fmt.Errorf("pack %q: %w", p.Name, err)
	}
	props, _ := raw["properties"].(map[string]any)
	for field, v := range props {
		n, ok := v.(map[string]any)
		if !ok {
			continue
		}
		ref, _ := n["$ref"].(string)
		url, ok := strings.CutPrefix(ref, packschema.PackScheme)
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(url, "#")
		held, ok := b.Packs[name]
		if !ok {
			return nil, fmt.Errorf("pack %q holds %q, which is not in the bundle", p.Name, name)
		}
		sub, err := b.inline(held)
		if err != nil {
			return nil, err
		}
		delete(n, "$ref")
		for k, v := range n { // the field's own keys over the held pack's
			sub[k] = v
		}
		props[field] = sub
	}
	return raw, nil
}

// translate sets every node's title, description and message to what its x-pack-i18n says in lang (or lang's
// short form); a node that says nothing in lang keeps the base language.
func translate(v any, lang string) {
	short, _, _ := strings.Cut(lang, "-")
	switch v := v.(type) {
	case map[string]any:
		if tr, ok := v[docs.KeyI18n].(map[string]any); ok {
			for _, l := range []string{lang, short} {
				if t, ok := tr[l].(map[string]any); ok {
					for _, key := range []string{"title", "description", "message"} {
						if s, ok := t[key].(string); ok && len(s) > 0 {
							v[key] = s
						}
					}
					break
				}
			}
		}
		for _, w := range v {
			translate(w, lang)
		}
	case []any:
		for _, w := range v {
			translate(w, lang)
		}
	}
}
