package emit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/pelletier/go-toml/v2"
	"sigs.k8s.io/yaml"
)

// funcMap is what a template may call: sprig's functions, as Helm has them, and the ones Helm adds to them —
// toYaml, toJson, toToml and their readers, required, include, tpl, lookup. As in Helm, env and expandenv are not
// there: a file rendered from a pack must not depend on the environment of the machine that renders it.
// lookup answers an empty map, as `helm template` does: the render knows no cluster.
//
// Not every function is deterministic (now, randAlphaNum, uuidv4 …, as in Helm): a template that calls one is the
// author's to answer for. Every other format of this package is.
func funcMap(t *template.Template) template.FuncMap {
	f := sprig.TxtFuncMap()
	delete(f, "env")
	delete(f, "expandenv")

	f["toYaml"] = toYAML
	f["mustToYaml"] = mustToYAML
	f["fromYaml"] = fromYAML
	f["fromYamlArray"] = fromYAMLArray
	f["toJson"] = toJSON
	f["mustToJson"] = mustToJSON
	f["toPrettyJson"] = toPrettyJSON
	f["fromJson"] = fromJSON
	f["fromJsonArray"] = fromJSONArray
	f["toToml"] = toTOML
	f["fromToml"] = fromTOML

	f["required"] = func(msg string, val any) (any, error) {
		switch v := val.(type) {
		case nil:
			return val, errors.New(msg)
		case string:
			if len(v) == 0 {
				return val, errors.New(msg)
			}
		}
		return val, nil
	}
	f["lookup"] = func(string, string, string, string) (map[string]any, error) { return map[string]any{}, nil }

	// include renders a named template of the same file ({{ define }}) and returns its text, to be piped on
	// (indent, nindent …). A template that includes itself is stopped.
	depth := map[string]int{}
	f["include"] = func(name string, data any) (string, error) {
		if depth[name] > 1000 {
			return "", fmt.Errorf("include %q: rendering it includes itself too deep", name)
		}
		depth[name]++
		defer func() { depth[name]-- }()
		var buf strings.Builder
		err := t.ExecuteTemplate(&buf, name, data)
		return buf.String(), err
	}
	// tpl renders a string as a template, with the same functions and the same named templates.
	f["tpl"] = func(text string, data any) (string, error) {
		c, err := t.Clone()
		if err != nil {
			return "", fmt.Errorf("tpl: %w", err)
		}
		if _, err := c.New("tpl").Parse(text); err != nil {
			return "", fmt.Errorf("tpl: %w", err)
		}
		var buf strings.Builder
		if err := c.ExecuteTemplate(&buf, "tpl", data); err != nil {
			return "", fmt.Errorf("tpl: %w", err)
		}
		return buf.String(), nil
	}
	return f
}

func toYAML(v any) string {
	s, err := mustToYAML(v)
	if err != nil {
		return ""
	}
	return s
}

func mustToYAML(v any) (string, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(b), "\n"), nil
}

// fromYAML reads a string as a map; a string that is not YAML gives a map with the key Error, as in Helm.
func fromYAML(s string) map[string]any {
	m := map[string]any{}
	if err := yaml.Unmarshal([]byte(s), &m); err != nil {
		m["Error"] = err.Error()
	}
	return m
}

func fromYAMLArray(s string) []any {
	var a []any
	if err := yaml.Unmarshal([]byte(s), &a); err != nil {
		return []any{err.Error()}
	}
	return a
}

func toJSON(v any) string {
	s, err := mustToJSON(v)
	if err != nil {
		return ""
	}
	return s
}

func mustToJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func toPrettyJSON(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return ""
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func fromJSON(s string) map[string]any {
	m := map[string]any{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		m["Error"] = err.Error()
	}
	return m
}

func fromJSONArray(s string) []any {
	var a []any
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		return []any{err.Error()}
	}
	return a
}

func toTOML(v any) string {
	b, err := marshalTOML(v)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(string(b), "\n")
}

func fromTOML(s string) map[string]any {
	m := map[string]any{}
	if err := toml.Unmarshal([]byte(s), &m); err != nil {
		m["Error"] = err.Error()
	}
	return m
}

// marshalTOML writes a table as TOML: keys sorted, tables for nested objects. Whole numbers are integers, as the
// other emitters write them (the values tree holds them as float64); TOML has no null.
func marshalTOML(v any) ([]byte, error) {
	n, err := tomlTree(v, "")
	if err != nil {
		return nil, err
	}
	if _, ok := n.(map[string]any); !ok {
		return nil, errors.New("toml: the node is not an object")
	}
	return toml.Marshal(n)
}

func tomlTree(v any, at string) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, fmt.Errorf("toml: %s: TOML has no null", strings.TrimPrefix(at, "."))
	case float64:
		if x == float64(int64(x)) {
			return int64(x), nil
		}
		return x, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			c, err := tomlTree(e, at+"."+k)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			c, err := tomlTree(e, fmt.Sprintf("%s[%d]", at, i))
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	}
	return v, nil
}
