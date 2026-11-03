package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// Compiled is the validating view of a schema: the same document with every
// `format: password` leaf swapped for the secretRef object, and its foreign
// $refs resolved to the documents the loader attached.
type Compiled struct {
	Validator *jsonschema.Schema
}

// Compile builds the validator. Foreign references are registered as resources under their own URLs, so the
// document itself is compiled unchanged apart from the password swap.
func (s *Schema) Compile(id string) (*Compiled, error) {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	seen := map[string]bool{id: true} // the document itself is added last, under id
	if err := s.addForeign(c, seen); err != nil {
		return nil, err
	}
	view, err := json.Marshal(validationView(s.Raw))
	if err != nil {
		return nil, err
	}
	if err := addResource(c, id, view); err != nil {
		return nil, err
	}
	v, err := c.Compile(id)
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}
	return &Compiled{Validator: v}, nil
}

func (s *Schema) addForeign(c *jsonschema.Compiler, seen map[string]bool) error {
	for url, f := range s.Foreign {
		if seen[url] {
			continue
		}
		seen[url] = true
		view, err := json.Marshal(validationView(f.Raw))
		if err != nil {
			return err
		}
		if err := addResource(c, url, view); err != nil {
			return err
		}
		if err := f.addForeign(c, seen); err != nil {
			return err
		}
	}
	return nil
}

func addResource(c *jsonschema.Compiler, url string, raw []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	return c.AddResource(url, doc)
}

// validationView returns a deep copy where every {format: password} node is
// replaced by a $ref to the secretRef definition, keeping title/description.
func validationView(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if f, ok := x["format"].(string); ok && f == "password" {
			out := secretRefSchema()
			for _, keep := range []string{"title", "description"} {
				if t, ok := x[keep]; ok {
					out[keep] = t
				}
			}
			return out
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = validationView(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = validationView(e)
		}
		return out
	}
	return v
}

// Validate checks an instance and returns every violation as path + message.
func (c *Compiled) Validate(instance any) []Violation {
	err := c.Validator.Validate(instance)
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []Violation{{Path: "", Message: err.Error()}}
	}
	var out []Violation
	flatten(ve, &out)
	if len(out) == 0 {
		out = append(out, Violation{Path: "/" + joinPath(ve.InstanceLocation), Message: ve.Error()})
	}
	return out
}

var printer = message.NewPrinter(language.English)

type Violation struct {
	Path    string
	Message string
	// Required: the violation is a missing required property — the operator's
	// to name, not a defect of the defaults.
	Required bool
	// Missing are the properties a Required violation misses, under Path.
	Missing []string
}

func (v Violation) String() string { return v.Path + ": " + v.Message }

func flatten(ve *jsonschema.ValidationError, out *[]Violation) {
	if len(ve.Causes) == 0 {
		v := Violation{Path: "/" + joinPath(ve.InstanceLocation), Message: ve.ErrorKind.LocalizedString(printer)}
		if r, ok := ve.ErrorKind.(*kind.Required); ok {
			v.Required, v.Missing = true, r.Missing
		}
		*out = append(*out, v)
		return
	}
	for _, c := range ve.Causes {
		flatten(c, out)
	}
}

func joinPath(segs []string) string {
	var out strings.Builder
	for i, s := range segs {
		if i > 0 {
			out.WriteString("/")
		}
		out.WriteString(escape(s))
	}
	return out.String()
}

// secretRefSchema is what a password leaf accepts: only a reference to a secret, never a value.
func secretRefSchema() map[string]any {
	str := map[string]any{"type": "string", "minLength": 1}
	return map[string]any{
		"type": "object", "required": []any{"secretRef"}, "additionalProperties": false,
		"properties": map[string]any{"secretRef": map[string]any{
			"type": "object", "required": []any{"name", "key"}, "additionalProperties": false,
			"properties": map[string]any{"name": str, "key": str},
		}},
	}
}
