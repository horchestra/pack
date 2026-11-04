package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/internal/valuesfile"
	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/sdk/docs"
	"github.com/horchestra/pack/sdk/schema"
)

func init() {
	fillCmd.Flags().BoolVar(&fillAccept, "accept-suggested", false, "take every suggestion of the pack, asking nothing")
	fillCmd.Flags().BoolVar(&fillRewrite, "rewrite", false, "write values.yaml even when it has comments of its own, which are lost")
	answerCmd.Flags().StringVar(&answerFile, "answers", "", "a JSON file of answers: {\"<pointer>\": value, …}")
	answerCmd.Flags().StringVar(&answerPatch, "patch", "", "a JSON Patch of the values (add, replace, remove): what a model made of what the operator said")
	answerCmd.Flags().StringVar(&answerSource, "source", valuesfile.FromOperator, "who answers: operator or llm (the values.yaml says it above the value)")
	answerCmd.Flags().BoolVar(&fillRewrite, "rewrite", false, "write values.yaml even when it has comments of its own, which are lost")
	valuesCmd.AddCommand(explainCmd)
	rootCmd.AddCommand(fillCmd, answerCmd)
}

var (
	fillAccept   bool
	fillRewrite  bool
	answerFile   string
	answerPatch  string
	answerSource string
)

var fillCmd = &cobra.Command{
	Use:   "fill",
	Short: "ask what the values do not give: the settings required, the ones the pack suggests a value for",
	Long: `Asks what the values do not give and should: every setting the pack requires that nothing
gives, every setting not given that the pack suggests a value for (its suggestion worked out
from the facts of the key's target, pack discover). In a terminal it asks one at a time — an
empty answer takes the first offered, - passes — and writes the answers into values.yaml.
Elsewhere, and with --json, it says the questions: pointer, reason, the field's schema and
documentation, the answers offered; pack answer gives the answers. --accept-suggested takes
every suggestion. An answer the schema or a rule refuses is not written. In the terminal an
answer is read as the field's type; a number takes that answer offered, =<text> is the text
itself (=2 is 2, not the second offered).`,
	Args: cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		rs, err := openAsk(ctx)
		if err != nil {
			return err
		}
		defer rs.s.Close()
		lang, _ := valuesLangOf(ctx) // an environment that does not read leaves the base language: the questions are no less
		var answers []answer
		switch {
		case fillAccept:
			for _, q := range rs.qs.Questions {
				if c := q.Candidates; len(c) > 0 && c[0].Source == render.FromSuggest {
					answers = append(answers, answer{Pointer: q.Pointer, Value: c[0].Value, Source: valuesfile.FromSuggest, Why: c[0].Why})
				}
			}
		case !jsonOut && isTerminal(os.Stdin):
			if answers, err = interview(rs.qs.Questions, lang, os.Stdin, rs.parse); err != nil {
				return err
			}
		default:
			printQuestions(rs.qs, lang)
			return result(rs.qs)
		}
		changes, err := rs.apply(ctx, answers)
		if err != nil {
			return err
		}
		return result(changes)
	}),
}

var answerCmd = &cobra.Command{
	Use:   "answer [<pointer> <value>]",
	Short: "answer a question of pack fill: a value for a setting, into values.yaml",
	Long: `Gives a value for a setting — the pointer of a question of pack fill, or any setting's — and
writes it into values.yaml, saying who answered (--source: operator, or llm) above it. The value
is read as the field's type: a number, true or false, a JSON object or list, text otherwise; a
credential is a reference, name/key or {"secretRef": {…}}. --answers gives many, --patch a JSON
Patch of the values (add, replace, remove): what a model made of what the operator said in
words. An answer the schema or a rule of the pack refuses is not written; what is still to be
given is no refusal.`,
	Args: cobra.RangeArgs(0, 2),
	Run: run(func(_ *cobra.Command, args []string) error {
		if answerSource != valuesfile.FromOperator && answerSource != valuesfile.FromLLM {
			return fmt.Errorf("--source %s: operator or llm", answerSource)
		}
		ctx, stop := interrupt.Context()
		defer stop()
		rs, err := openAsk(ctx)
		if err != nil {
			return err
		}
		defer rs.s.Close()
		var answers []answer
		switch {
		case len(args) == 2:
			v, err := rs.parse(args[0], args[1])
			if err != nil {
				return err
			}
			answers = append(answers, answer{Pointer: args[0], Value: v, Source: answerSource})
		case len(answerFile) > 0:
			if answers, err = rs.answersFile(answerFile); err != nil {
				return err
			}
		case len(answerPatch) > 0:
			if answers, err = rs.patchFile(answerPatch); err != nil {
				return err
			}
		default:
			return errors.New("an answer: <pointer> <value>, --answers file or --patch file")
		}
		changes, err := rs.apply(ctx, answers)
		if err != nil {
			return err
		}
		return result(changes)
	}),
}

var explainCmd = &cobra.Command{
	Use:   "explain <pointer>",
	Short: "a setting: its value and where it came from, its default, formula and suggestion, its documentation",
	Args:  cobra.ExactArgs(1),
	Run: run(func(_ *cobra.Command, args []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		rs, err := openAsk(ctx)
		if err != nil {
			return err
		}
		defer rs.s.Close()
		ex, err := rs.explain(args[0])
		if err != nil {
			return err
		}
		lang, _ := valuesLangOf(ctx) // an environment that does not read leaves the base language
		fmt.Println(ex.Pointer)
		if d := textOf(ex.Schema, lang, "description"); len(d) > 0 {
			fmt.Println("  " + strings.ReplaceAll(d, "\n", "\n  "))
		}
		if ex.Given {
			fmt.Printf("  value: %s (%s", jsonText(ex.Value), ex.Source)
			if len(ex.Why) > 0 {
				fmt.Print(" — " + ex.Why)
			}
			fmt.Println(")")
		} else {
			fmt.Println("  not given")
		}
		if ex.Default != nil {
			fmt.Println("  default:", jsonText(ex.Default))
		}
		if len(ex.Formula) > 0 {
			fmt.Println("  filled by a formula when not given:", ex.Formula)
		}
		for _, c := range ex.Offered {
			fmt.Printf("  offered: %s (%s%s)\n", jsonText(c.Value), c.Source, whyOf(c.Why))
		}
		return result(ex)
	}),
}

// asking is a release opened to be asked: its session, pack and values file, and the questions they leave.
type asking struct {
	s  *session.Session
	b  *bundle.Bundle
	f  *valuesFile
	qs *render.Questions
}

func openAsk(ctx context.Context) (*asking, error) {
	f, err := readValuesFile()
	if err != nil {
		return nil, err
	}
	s, err := open(ctx, true)
	if err != nil {
		return nil, err
	}
	ref, err := dir.Release.PackRef()
	if err == nil {
		ref, err = s.Pinned(ref)
	}
	var b *bundle.Bundle
	if err == nil {
		b, err = s.Pack(ref)
	}
	var qs *render.Questions
	if err == nil {
		qs, err = render.Ask(b, render.Values{Values: f.values}, s.Ctx)
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	return &asking{s: s, b: b, f: f, qs: qs}, nil
}

// answer is a value for a setting, and who gave it.
type answer struct {
	Pointer string
	Value   any  // nil with Remove
	Remove  bool // a patch's remove: the setting is not given any more
	Source  string
	Why     string
}

// change is what an answer changed.
type change struct {
	Pointer string `json:"pointer"`
	From    any    `json:"from,omitempty"`
	To      any    `json:"to,omitempty"`
	Source  string `json:"source"`
}

// node is the schema of a setting of the operator's pointer.
func (rs *asking) node(ptr string) (*schema.Node, error) {
	if !strings.HasPrefix(ptr, "/") {
		return nil, fmt.Errorf("%q: a setting is a JSON Pointer, /…", ptr)
	}
	_, n, err := rs.b.Composition.Lookup(rs.b.Internal(ptr))
	if err != nil || n == nil {
		return nil, fmt.Errorf("%s: no such setting in %s", ptr, rs.b.Name)
	}
	return n, nil
}

// parse reads an answer given as text the way the field's type says.
func (rs *asking) parse(ptr, text string) (any, error) {
	n, err := rs.node(ptr)
	if err != nil {
		return nil, err
	}
	if n.IsPassword() {
		return secretRef(ptr, text)
	}
	types := n.Types
	switch {
	case slices.Contains(types, "integer"):
		v, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not an integer", ptr, text)
		}
		return v, nil
	case slices.Contains(types, "number"):
		v, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a number", ptr, text)
		}
		return v, nil
	case slices.Contains(types, "boolean"):
		v, err := strconv.ParseBool(text)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not true or false", ptr, text)
		}
		return v, nil
	case slices.Contains(types, "object"), slices.Contains(types, "array"):
		var v any
		if err := json.Unmarshal([]byte(text), &v); err != nil {
			return nil, fmt.Errorf("%s: %q is not JSON: %w", ptr, text, err)
		}
		return v, nil
	}
	return text, nil
}

// secretRef is a credential's answer: a reference — name/key, or the reference as JSON — never the credential.
func secretRef(ptr string, v any) (any, error) {
	if text, ok := v.(string); ok {
		if strings.HasPrefix(strings.TrimSpace(text), "{") {
			var m any
			if err := json.Unmarshal([]byte(text), &m); err != nil {
				return nil, fmt.Errorf("%s: %w", ptr, err)
			}
			v = m
		} else if name, key, ok := strings.Cut(text, "/"); ok {
			if !secretName.MatchString(name) || !secretName.MatchString(key) {
				return nil, fmt.Errorf("%s: %q: a reference is name/key, each letters, digits, '.', '_', '-'", ptr, text)
			}
			return map[string]any{"secretRef": map[string]any{"name": name, "key": key}}, nil
		}
	}
	if m, ok := v.(map[string]any); ok {
		if ref, ok := m["secretRef"].(map[string]any); ok && len(ref) > 0 {
			return m, nil
		}
	}
	return nil, fmt.Errorf("%s is a credential: answer it with a reference to one kept elsewhere, name/key or {\"secretRef\": {\"name\", \"key\"}}, never the credential", ptr)
}

var secretName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// typed is a value of an answers file or a patch as the field takes it: a credential's checked to be a reference.
func (rs *asking) typed(ptr string, v any) (any, error) {
	n, err := rs.node(ptr)
	if err != nil {
		return nil, err
	}
	if n.IsPassword() {
		return secretRef(ptr, v)
	}
	return v, nil
}

func (rs *asking) answersFile(path string) ([]answer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []answer
	for _, ptr := range slices.Sorted(maps.Keys(m)) {
		v, err := rs.typed(ptr, m[ptr])
		if err != nil {
			return nil, err
		}
		out = append(out, answer{Pointer: ptr, Value: v, Source: answerSource})
	}
	return out, nil
}

func (rs *asking) patchFile(path string) ([]answer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ops []struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value any    `json:"value"`
	}
	if err := json.Unmarshal(raw, &ops); err != nil {
		return nil, fmt.Errorf("%s: a JSON Patch, [{op, path, value}]: %w", path, err)
	}
	var out []answer
	for i, op := range ops {
		switch op.Op {
		case "add", "replace":
			v, err := rs.typed(op.Path, op.Value)
			if err != nil {
				return nil, err
			}
			out = append(out, answer{Pointer: op.Path, Value: v, Source: answerSource})
		case "remove":
			if _, err := rs.node(op.Path); err != nil {
				return nil, err
			}
			out = append(out, answer{Pointer: op.Path, Remove: true, Source: answerSource})
		default:
			return nil, fmt.Errorf("%s: operation %d: %q — add, replace or remove", path, i, op.Op)
		}
	}
	return out, nil
}

// apply writes the answers into the values, unless they make the values wrong — a problem of the schema or of a
// rule that was not there — and says what changed.
func (rs *asking) apply(ctx context.Context, answers []answer) ([]change, error) {
	if len(answers) == 0 {
		fmt.Println("nothing answered")
		return []change{}, nil
	}
	values, _ := schema.DeepCopy(orEmptyMap(rs.f.values)).(map[string]any)
	src, err := valuesfile.LoadSources(dir.Path)
	if err != nil {
		return nil, err
	}
	var changes []change
	for _, a := range answers {
		from, had := getPointer(values, a.Pointer)
		if a.Remove {
			if !had {
				continue
			}
			removePointer(values, a.Pointer)
			delete(src, a.Pointer)
			changes = append(changes, change{Pointer: a.Pointer, From: from, Source: a.Source})
			continue
		}
		if had && sameValue(from, a.Value) {
			continue
		}
		if err := setPointer(values, a.Pointer, a.Value); err != nil {
			return nil, err
		}
		src[a.Pointer] = valuesfile.Source{Value: a.Value, From: a.Source, Why: a.Why}
		changes = append(changes, change{Pointer: a.Pointer, From: from, To: a.Value, Source: a.Source})
	}
	after, err := render.Ask(rs.b, render.Values{Values: values}, rs.s.Ctx)
	if err != nil {
		return nil, err
	}
	was := map[string]bool{}
	for _, p := range rs.qs.Problems {
		was[p.String()] = true
	}
	var made []render.Problem
	for _, p := range after.Problems {
		if !was[p.String()] {
			made = append(made, p)
		}
	}
	if len(made) > 0 {
		lines := make([]string, len(made))
		for i, p := range made {
			lines[i] = p.String()
		}
		return nil, withProblems(api.CodeValuesInvalid, fmt.Errorf("the answers make the values wrong, nothing is written:\n  %s", strings.Join(lines, "\n  ")), made)
	}
	msg, err := rs.f.save(ctx, rs.b, values, src, fillRewrite)
	if err != nil {
		return nil, err
	}
	for _, c := range changes {
		fmt.Printf("%s: %s → %s (%s)\n", c.Pointer, jsonText(c.From), jsonText(c.To), c.Source)
	}
	fmt.Println(msg)
	if left := len(after.Questions); left > 0 {
		fmt.Printf("%d question(s) left: pack fill\n", left)
	}
	return changes, nil
}

// interview asks the questions one by one: an empty answer takes the first offered, - passes, a number takes that
// one offered, =<text> is the text itself, anything else is the value — read as the field's type by parse, and asked
// again when it does not read. The end of the input ends it, the questions after unanswered.
func interview(qs []render.Question, lang string, in io.Reader, parse func(ptr, text string) (any, error)) ([]answer, error) {
	r := bufio.NewReader(in)
	var out []answer
	for _, q := range qs {
		fmt.Println(questionText(q, lang))
		for {
			fmt.Print("answer: ")
			line, err := r.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			if errors.Is(err, io.EOF) && len(strings.TrimSpace(line)) == 0 {
				return out, nil // the input ended: nothing more is answered
			}
			line = strings.TrimSpace(line)
			if line == "-" || len(line) == 0 && len(q.Candidates) == 0 {
				break
			}
			if len(line) == 0 {
				out = append(out, fromCandidate(q, q.Candidates[0]))
				break
			}
			if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(q.Candidates) {
				out = append(out, fromCandidate(q, q.Candidates[n-1]))
				break
			}
			v, perr := parse(q.Pointer, strings.TrimPrefix(line, "="))
			if perr != nil {
				fmt.Println(" ", perr)
				if errors.Is(err, io.EOF) {
					return out, nil
				}
				continue
			}
			out = append(out, answer{Pointer: q.Pointer, Value: v, Source: valuesfile.FromOperator})
			break
		}
	}
	return out, nil
}

// sameValue: two values mean the same, whatever Go type a number has.
func sameValue(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

func fromCandidate(q render.Question, c render.Candidate) answer {
	src := valuesfile.FromOperator
	if c.Source == render.FromSuggest {
		src = valuesfile.FromSuggest
	}
	return answer{Pointer: q.Pointer, Value: c.Value, Source: src, Why: c.Why}
}

func printQuestions(qs *render.Questions, lang string) {
	for _, q := range qs.Questions {
		fmt.Println(questionText(q, lang))
	}
	for _, p := range qs.Problems {
		fmt.Println("problem:", p)
	}
	if len(qs.Questions) == 0 {
		fmt.Println("nothing to ask")
	} else {
		fmt.Println("pack answer <pointer> <value> answers one")
	}
}

func questionText(q render.Question, lang string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)", q.Pointer, q.Reason)
	if d := textOf(q.Schema, lang, "description"); len(d) > 0 {
		b.WriteString(": " + strings.ReplaceAll(d, "\n", " "))
	}
	if q.Secret {
		b.WriteString("\n  a credential: name/key of a secret kept elsewhere")
	}
	for i, c := range q.Candidates {
		fmt.Fprintf(&b, "\n  %d. %s (%s%s)", i+1, jsonText(c.Value), c.Source, whyOf(c.Why))
	}
	return b.String()
}

// textOf is a text of the field in lang, the base language's when lang has none.
func textOf(sch map[string]any, lang, key string) string {
	if len(lang) > 0 {
		if tr, ok := sch[docs.KeyI18n].(map[string]any); ok {
			short, _, _ := strings.Cut(lang, "-")
			for _, l := range []string{lang, short} {
				if t, ok := tr[l].(map[string]any); ok {
					if s, ok := t[key].(string); ok && len(s) > 0 {
						return s
					}
				}
			}
		}
	}
	s, _ := sch[key].(string)
	return s
}

func whyOf(why string) string {
	if len(why) == 0 {
		return ""
	}
	return " — " + why
}

func jsonText(v any) string {
	if v == nil {
		return "—"
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (rs *asking) explain(ptr string) (*api.Explanation, error) {
	n, err := rs.node(ptr)
	if err != nil {
		return nil, err
	}
	ex := &api.Explanation{Pointer: ptr, Schema: n.Raw, Formula: n.Compute}
	if n.HasDefault {
		ex.Default = n.Default
	}
	if v, ok := getPointer(orEmptyMap(rs.f.values), ptr); ok {
		src, err := valuesfile.LoadSources(dir.Path)
		if err != nil {
			return nil, err
		}
		s := src.Of(ptr, v)
		ex.Given, ex.Value, ex.Source, ex.Why = true, v, s.From, s.Why
	}
	for _, q := range rs.qs.Questions {
		if q.Pointer == ptr {
			ex.Offered = q.Candidates
		}
	}
	return ex, nil
}

// ---- values by pointer

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func getPointer(doc map[string]any, ptr string) (any, bool) {
	var cur any = doc
	for _, seg := range schema.Split(ptr) {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[seg]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func setPointer(doc map[string]any, ptr string, v any) error {
	segs := schema.Split(ptr)
	if len(segs) == 0 {
		return fmt.Errorf("%q: not a setting", ptr)
	}
	cur := doc
	for _, seg := range segs[:len(segs)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			if _, there := cur[seg]; there {
				return fmt.Errorf("%s: %s is not an object in the values", ptr, seg)
			}
			next = map[string]any{}
			cur[seg] = next
		}
		cur = next
	}
	cur[segs[len(segs)-1]] = v
	return nil
}

func removePointer(doc map[string]any, ptr string) {
	segs := schema.Split(ptr)
	cur := doc
	for _, seg := range segs[:len(segs)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	delete(cur, segs[len(segs)-1])
}
