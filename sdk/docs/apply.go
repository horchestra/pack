package docs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/horchestra/pack/sdk/packschema"
	"github.com/invopop/jsonschema"
)

// Apply writes the documentation into a pack's schema: the base language into title, description and examples, the
// others into x-pack-i18n, the groups into x-pack-group and the root's x-pack-groups, the rules' messages by id. A
// pointer, a group or a rule the schema has not is an error: a renamed field leaves no stale text behind.
func (s *Set) Apply(root *jsonschema.Schema) error {
	for _, f := range append([]*File{s.Base}, s.Others...) {
		for _, ptr := range f.Order {
			e := f.Fields[ptr]
			node, err := Lookup(root, ptr)
			if err != nil {
				return fmt.Errorf("%s/%s.yaml: %s: %w", Dir, f.Lang, ptr, err)
			}
			if f.Base {
				if len(e.Title) > 0 {
					node.Title = e.Title
				}
				if len(e.Description) > 0 {
					node.Description = e.Description
				}
				if len(e.Examples) > 0 {
					ex, err := examples(node, e.Examples)
					if err != nil {
						return fmt.Errorf("%s/%s.yaml: %s: examples: %w", Dir, f.Lang, ptr, err)
					}
					node.Examples = ex
				}
				if len(e.Group) > 0 {
					extras(node)[KeyGroup] = e.Group
				}
				continue
			}
			if t := texts("title", e.Title, "description", e.Description); len(t) > 0 {
				i18n(node)[f.Lang] = t
			}
		}
	}
	if err := s.groups(root); err != nil {
		return err
	}
	if err := s.rules(root); err != nil {
		return err
	}
	extras(root)[KeyLangs] = s.Langs()
	return nil
}

// groups lists the groups in the order the fields first use them, with their titles.
func (s *Set) groups(root *jsonschema.Schema) error {
	var used []string
	Walk(root, func(_ string, n *jsonschema.Schema) {
		if g, ok := n.Extras[KeyGroup].(string); ok && !contains(used, g) {
			used = append(used, g)
		}
	})
	var list []any
	for _, id := range used {
		title := s.Base.Groups[id]
		if len(title) == 0 {
			return fmt.Errorf("%s/%s.yaml: group %q has no title under groups:", Dir, s.Base.Lang, id)
		}
		g := map[string]any{"id": id, "title": title}
		tr := map[string]map[string]string{}
		for _, f := range s.Others {
			if t := f.Groups[id]; len(t) > 0 {
				tr[f.Lang] = map[string]string{"title": t}
			}
		}
		if len(tr) > 0 {
			g[KeyI18n] = tr
		}
		list = append(list, g)
	}
	for id := range s.Base.Groups {
		if !contains(used, id) {
			return fmt.Errorf("%s/%s.yaml: group %q: no field is in it", Dir, s.Base.Lang, id)
		}
	}
	if len(list) > 0 {
		extras(root)[KeyGroups] = list
	}
	return nil
}

// rules says the messages of the rules by their id.
func (s *Set) rules(root *jsonschema.Schema) error {
	byID := map[string]*packschema.Rule{}
	var bad []string
	Walk(root, func(ptr string, n *jsonschema.Schema) {
		v, ok := n.Extras["x-pack-rules"]
		if !ok {
			return
		}
		// the rules are the slice Reflect hung on the node, its own copy: their messages are written in place
		rs, ok := v.([]packschema.Rule)
		if !ok {
			bad = append(bad, ptr)
			return
		}
		for i := range rs {
			if len(rs[i].ID) > 0 {
				byID[rs[i].ID] = &rs[i]
			}
		}
	})
	if len(bad) > 0 {
		return fmt.Errorf("x-pack-rules of %s: not the []packschema.Rule Reflect hangs on a node", strings.Join(bad, ", "))
	}
	for _, f := range append([]*File{s.Base}, s.Others...) {
		for id, msg := range f.Rules {
			r, ok := byID[id]
			if !ok {
				return fmt.Errorf("%s/%s.yaml: rules: %q is not the id of a rule of the pack", Dir, f.Lang, id)
			}
			switch {
			case len(msg) == 0:
			case f.Base:
				r.Message = msg
			default:
				if r.I18n == nil {
					r.I18n = map[string]map[string]string{}
				}
				r.I18n[f.Lang] = map[string]string{"message": msg}
			}
		}
	}
	return nil
}

var (
	integer  = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
	fraction = regexp.MustCompile(`^-?(0|[1-9][0-9]*)\.[0-9]*[1-9]$`)
)

// NumberRule is what a number of examples may be, in every SDK alike: an integer that is an int64, or a decimal
// fraction without an exponent or a trailing zero, of at most 15 significant digits and not under 0.0001 — the
// text each SDK writes back unchanged, so schema.json is the same byte for byte whichever wrote it.
const NumberRule = "an integer of int64, or -?digits.digits: no exponent, no trailing zero, at most 15 significant digits, at least 0.0001"

// canonicalFraction says whether x (matching fraction) is a fraction every SDK writes back as x.
func canonicalFraction(x string) bool {
	whole, frac, _ := strings.Cut(strings.TrimPrefix(x, "-"), ".")
	if whole == "0" && len(frac)-len(strings.TrimLeft(frac, "0")) > 3 {
		return false // under 0.0001: an exponent in some SDK's writer
	}
	return len(strings.TrimLeft(whole+frac, "0")) <= 15
}

// examples are typed by the field: a list of texts in the file, numbers or booleans in the schema when the field
// is one. A field of another type has no examples in docs/.
func examples(node *jsonschema.Schema, list []string) ([]any, error) {
	if len(node.Ref) > 0 || node.Format == "password" || node.WriteOnly {
		return nil, fmt.Errorf("a held pack and a secret have no examples")
	}
	out := make([]any, 0, len(list))
	for _, x := range list {
		switch node.Type {
		case "string":
			out = append(out, x)
		case "integer", "number":
			if integer.MatchString(x) {
				n, err := strconv.ParseInt(x, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("%q: %s", x, NumberRule)
				}
				out = append(out, n)
				continue
			}
			if node.Type == "integer" || !fraction.MatchString(x) || !canonicalFraction(x) {
				return nil, fmt.Errorf("%q: %s", x, NumberRule)
			}
			out = append(out, json.Number(x)) // written as it is
		case "boolean":
			if x != "true" && x != "false" {
				return nil, fmt.Errorf("%q is not true or false", x)
			}
			out = append(out, x == "true")
		default:
			return nil, fmt.Errorf("only a string, a number or a boolean has examples here")
		}
	}
	return out, nil
}

// Lookup finds the node of a JSON Pointer of a field: "/" is the pack itself. The fields of a held pack are its own
// docs/', not the holder's.
func Lookup(root *jsonschema.Schema, ptr string) (*jsonschema.Schema, error) {
	if ptr == "/" {
		return root, nil
	}
	node := root
	for seg := range strings.SplitSeq(strings.TrimPrefix(ptr, "/"), "/") {
		seg = strings.NewReplacer("~1", "/", "~0", "~").Replace(seg)
		if len(node.Ref) > 0 {
			return nil, fmt.Errorf("a field of a held pack: it is documented in that pack's %s/", Dir)
		}
		if node.Properties == nil {
			return nil, fmt.Errorf("%q: not an object", seg)
		}
		next, ok := node.Properties.Get(seg)
		if !ok {
			return nil, fmt.Errorf("%q: no such field", seg)
		}
		node = next
	}
	return node, nil
}

// Walk visits the pack and its fields, depth first in the order of the schema, with their pointers ("/" the pack);
// it does not go into a held pack.
func Walk(root *jsonschema.Schema, f func(ptr string, n *jsonschema.Schema)) {
	var walk func(ptr string, n *jsonschema.Schema)
	walk = func(ptr string, n *jsonschema.Schema) {
		f(ptr, n)
		if n.Properties == nil || len(n.Ref) > 0 {
			return
		}
		prefix := strings.TrimSuffix(ptr, "/")
		for p := n.Properties.Oldest(); p != nil; p = p.Next() {
			walk(prefix+"/"+strings.NewReplacer("~", "~0", "/", "~1").Replace(p.Key), p.Value)
		}
	}
	walk("/", root)
}

func texts(kv ...string) map[string]string {
	out := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		if len(kv[i+1]) > 0 {
			out[kv[i]] = kv[i+1]
		}
	}
	return out
}

func extras(s *jsonschema.Schema) map[string]any {
	if s.Extras == nil {
		s.Extras = map[string]any{}
	}
	return s.Extras
}

func i18n(s *jsonschema.Schema) map[string]map[string]string {
	m, ok := extras(s)[KeyI18n].(map[string]map[string]string)
	if !ok {
		m = map[string]map[string]string{}
		s.Extras[KeyI18n] = m
	}
	return m
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
