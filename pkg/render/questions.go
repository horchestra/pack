package render

import (
	"errors"
	"reflect"
	"slices"
	"sort"

	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/sdk/compute"
	"github.com/horchestra/pack/sdk/packschema"
	"github.com/horchestra/pack/sdk/schema"
)

// The reasons a setting is asked for.
const (
	ReasonRequired  = "required"  // the schema requires it, and nothing gives it
	ReasonSuggested = "suggested" // the operator did not give it, and the pack suggests a value for it
)

// The sources of a candidate answer.
const (
	FromSuggest = "suggest" // the pack's x-pack-suggest, with why
	FromDefault = "default" // the schema's default: what it is when nothing is given
	FromEnum    = "enum"    // one of the values the schema allows
	FromExample = "example" // an example of the documentation (docs/)
)

// Candidate is an answer offered for a question.
type Candidate struct {
	Value  any    `json:"value"`
	Source string `json:"source"`
	Why    string `json:"why,omitempty"`
}

// Question is a setting the values do not give that someone should answer: where (the operator's JSON Pointer, the
// question's id), why it is asked, what the field is (the schema's words for it, its documentation with them), and
// the answers offered, the pack's suggestion first.
type Question struct {
	Pointer    string         `json:"pointer"`
	Key        string         `json:"key"`
	Reason     string         `json:"reason"`
	Schema     map[string]any `json:"schema"`
	Secret     bool           `json:"secret,omitempty"` // a credential: the answer is a reference, {secretRef: {name, key}}
	Candidates []Candidate    `json:"candidates,omitempty"`
}

// Questions are what Ask finds: the questions, and the problems of the values that are no question (a value the
// schema or a rule refuses).
type Questions struct {
	Questions []Question `json:"questions"`
	Problems  []Problem  `json:"problems,omitempty"`
}

// schemaWords are what of a field's schema a question carries: enough to answer it, constrained.
var schemaWords = []string{"type", "enum", "const", "pattern", "format", "minimum", "maximum", "minLength", "maxLength",
	"items", "title", "description", "examples", "default", "x-pack-i18n", "writeOnly"}

// Ask renders the values as far as they go and says what is to be answered: every setting required and not given,
// every setting not given that the pack suggests a value for — the suggestion evaluated on the effective document,
// with the facts of the key's target. What else is wrong is in Problems; nothing of it stops Ask.
func Ask(b *bundle.Bundle, v Values, ctx Context) (*Questions, error) {
	r, err := newRender(b, ctx)
	if err != nil {
		return nil, err
	}
	if err := r.configure(v); err != nil {
		return nil, err
	}
	if err := r.resolve(); err != nil {
		return nil, err
	}
	if err := r.checkReach(); err != nil {
		return nil, err
	}
	if err := r.collect(); err != nil {
		return nil, err
	}
	out := &Questions{}
	gather := func(err error) error {
		var ps *Problems
		if errors.As(err, &ps) {
			out.Problems = append(out.Problems, ps.Problems...)
			return nil
		}
		return err
	}
	if err := gather(r.checkRules(packschema.PreMutate)); err != nil {
		return nil, err
	}
	if err := r.provide(); err != nil {
		return nil, err
	}
	if err := r.mutate(); err != nil {
		return nil, err
	}
	if err := gather(r.checkRules(packschema.PostMutate)); err != nil {
		return nil, err
	}
	if err := gather(r.validate()); err != nil {
		return nil, err
	}

	byPtr := map[string]*Question{}
	ask := func(internal, reason string) *Question {
		if q, ok := byPtr[internal]; ok {
			return q
		}
		q := &Question{Pointer: r.where(internal), Key: schema.Split(internal)[0], Reason: reason, Schema: map[string]any{}}
		if _, n, err := r.b.Composition.Lookup(internal); err == nil && n != nil {
			for _, w := range schemaWords {
				if x, ok := n.Raw[w]; ok {
					q.Schema[w] = x
				}
			}
			q.Secret = n.IsPassword()
		}
		byPtr[internal] = q
		return q
	}
	problems := out.Problems[:0]
	for _, p := range out.Problems {
		if p.Kind == KindRequired {
			ask(r.b.Internal(p.Where), ReasonRequired)
			continue
		}
		problems = append(problems, p)
	}
	out.Problems = problems
	for _, s := range r.suggests {
		if s.instance || r.userSet[s.ptr] || userWroteAbove(r.userSet, s.ptr) {
			continue
		}
		val, err := r.eval(&s.formula, r.doc, nil)
		if err != nil {
			out.Problems = append(out.Problems, Problem{Kind: KindRule, Where: r.where(s.ptr), Message: "x-pack-suggest: " + err.Error()})
			continue
		}
		if val == nil {
			continue // nothing to offer here
		}
		q := ask(s.ptr, ReasonSuggested)
		q.Candidates = append(q.Candidates, Candidate{Value: val, Source: FromSuggest, Why: s.why})
	}
	for _, q := range byPtr {
		if d, ok := q.Schema["default"]; ok {
			q.Candidates = append(q.Candidates, Candidate{Value: d, Source: FromDefault})
		}
		if e, ok := q.Schema["enum"].([]any); ok {
			for _, x := range e {
				q.Candidates = append(q.Candidates, Candidate{Value: x, Source: FromEnum})
			}
		}
		if ex, ok := q.Schema["examples"].([]any); ok {
			for _, x := range ex {
				if !slices.ContainsFunc(q.Candidates, func(c Candidate) bool { return reflect.DeepEqual(c.Value, x) }) {
					q.Candidates = append(q.Candidates, Candidate{Value: x, Source: FromExample})
				}
			}
		}
		out.Questions = append(out.Questions, *q)
	}
	sort.Slice(out.Questions, func(i, j int) bool { return out.Questions[i].Pointer < out.Questions[j].Pointer })
	return out, nil
}

// Eval works an expression out as a formula of the key would be — with its values (the subtree of the key's pack),
// the facts of its target, the other keys — on the values rendered as far as they go: what a diagnosis's fix reads.
func Eval(b *bundle.Bundle, v Values, ctx Context, key, expr string) (any, error) {
	r, err := newRender(b, ctx)
	if err != nil {
		return nil, err
	}
	if err := r.configure(v); err != nil {
		return nil, err
	}
	if err := r.resolve(); err != nil {
		return nil, err
	}
	if err := r.collect(); err != nil {
		return nil, err
	}
	if err := r.mutate(); err != nil {
		return nil, err
	}
	a, err := compute.Compile(r.env, expr)
	if err != nil {
		return nil, err
	}
	return r.eval(&formula{ptr: "/" + key, root: "/" + key, key: key, ast: a, reads: compute.Reads(a)}, r.doc, nil)
}
