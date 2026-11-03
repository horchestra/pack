package session

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/sdk/packschema"
)

// PhaseAction is the phase of a step that runs an action: a hook outside of any saga of versions.
const PhaseAction = "action"

// Action is an action of the installed version on one of its keys.
type Action struct {
	Key    string            `json:"key"`
	Pack   string            `json:"pack"`
	Action packschema.Action `json:"action"`
}

// Actions are the actions of the installed version, by key in the order of the keys, the keys the release has.
func Actions(b *bundle.Bundle, rel *render.Release) []Action {
	if b == nil || rel == nil {
		return nil
	}
	var out []Action
	for _, key := range b.Keys {
		if !slices.ContainsFunc(rel.Units, func(u render.Unit) bool { return u.Key == key }) {
			continue
		}
		p := b.Packs[b.KeyPack[key]]
		if p == nil || p.Schema == nil {
			continue
		}
		for _, a := range p.Schema.Root.Actions {
			out = append(out, Action{Key: key, Pack: p.Name, Action: a})
		}
	}
	return out
}

// FindAction is the action of that name — on key, when several keys have one of the name.
func FindAction(all []Action, name, key string) (Action, error) {
	var found []Action
	var names []string
	for _, a := range all {
		if !slices.Contains(names, a.Action.Name) {
			names = append(names, a.Action.Name)
		}
		if a.Action.Name == name && (len(key) == 0 || a.Key == key) {
			found = append(found, a)
		}
	}
	switch {
	case len(found) == 1:
		return found[0], nil
	case len(found) > 1:
		var keys []string
		for _, a := range found {
			keys = append(keys, a.Key)
		}
		return Action{}, fmt.Errorf("%s is an action of %s: say which with --key", name, strings.Join(keys, ", "))
	case len(names) == 0:
		return Action{}, fmt.Errorf("the installed version has no actions")
	case len(key) > 0:
		return Action{}, fmt.Errorf("%s has no action %s (the actions: %s)", key, name, strings.Join(names, ", "))
	}
	return Action{}, fmt.Errorf("no action %s (the actions: %s)", name, strings.Join(names, ", "))
}

// Params are the parameters given as name=value, typed by the action's schema, the defaults with them: an unknown
// name, a value not of its type or not of its enum, a required one not given — refused.
func (a Action) Params(given []string) (map[string]any, error) {
	props := map[string]packschema.Param{}
	var required []string
	if s := a.Action.Schema; s != nil {
		props, required = s.Properties, s.Required
	}
	out := map[string]any{}
	for _, kv := range given {
		name, raw, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("--param %s: name=value", kv)
		}
		p, known := props[name]
		if !known {
			return nil, fmt.Errorf("--param %s: %s has no parameter %s (%s)", kv, a.Action.Name, name, paramNames(props))
		}
		v, err := typed(p.Type, raw)
		if err != nil {
			return nil, fmt.Errorf("--param %s: %w", name, err)
		}
		if len(p.Enum) > 0 && !slices.ContainsFunc(p.Enum, func(e any) bool { return fmt.Sprint(e) == fmt.Sprint(v) }) {
			return nil, fmt.Errorf("--param %s: one of %v", name, p.Enum)
		}
		out[name] = v
	}
	for name, p := range props {
		if _, ok := out[name]; !ok && p.Default != nil {
			out[name] = p.Default
		}
	}
	for _, r := range required {
		if _, ok := out[r]; !ok {
			return nil, fmt.Errorf("%s needs --param %s=…", a.Action.Name, r)
		}
	}
	return out, nil
}

func paramNames(props map[string]packschema.Param) string {
	if len(props) == 0 {
		return "it takes none"
	}
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	slices.Sort(names)
	return "it takes " + strings.Join(names, ", ")
}

func typed(kind, raw string) (any, error) {
	switch kind {
	case "integer":
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", raw)
		}
		return n, nil
	case "number":
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", raw)
		}
		return f, nil
	case "boolean":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("%q is not true or false", raw)
		}
		return b, nil
	}
	return raw, nil
}

// Plan is the action as a plan of one step: the hook it is on its key, with the installed version's image and the
// command its parameters make — what run.approve and --approve look at, by its digest. An action that changes the
// installation is a plan to the policy; one the pack says is never run unasked is a risk.
func (a Action) Plan(b *bundle.Bundle, rel *render.Release, params map[string]any) (*plan.Plan, error) {
	cmd := make([]string, len(a.Action.Command))
	for i, w := range a.Action.Command {
		t, err := template.New("").Option("missingkey=error").Parse(w)
		if err != nil {
			return nil, fmt.Errorf("%s: command %d: %w", a.Action.Name, i, err)
		}
		var sb strings.Builder
		if err := t.Execute(&sb, params); err != nil {
			return nil, fmt.Errorf("%s: command %d: %w", a.Action.Name, i, err)
		}
		cmd[i] = sb.String()
	}
	image := ""
	if p := b.Packs[a.Pack]; p != nil {
		image = p.ImageRef
	}
	step := plan.Step{N: 1, Key: a.Key, Kind: plan.Hook, Name: a.Action.Name, Phase: PhaseAction, Timeout: a.Action.Timeout,
		Image: image, Command: cmd, Target: rel.Report.Targets[a.Key]}
	p := &plan.Plan{Pack: b.Name, From: b.Version, To: b.Version, Direction: plan.DirAction, Steps: []plan.Step{step}}
	plan.Seal(p, rel, rel)
	if a.Action.Approve {
		p.Risks = append(p.Risks, plan.Risk{Kind: plan.RiskAction, Key: a.Key, Why: a.Action.Name + " is never run unasked"})
	}
	return p, nil
}

// RunAction performs the plan of an action on the installed version: the hook on its key's target, with the key's
// unit and credentials, its lines on the way, all of it in the journal as a run of its own.
func (s *Session) RunAction(ctx context.Context, rel *render.Release, p *plan.Plan) (err error) {
	st := p.Steps[0]
	var u *render.Unit
	if i := slices.IndexFunc(rel.Units, func(u render.Unit) bool { return u.Key == st.Key }); i >= 0 {
		u = &rel.Units[i]
	}
	var creds []render.Credential
	for _, c := range rel.Credentials {
		if c.Key == st.Key {
			creds = append(creds, c)
		}
	}
	x := target.Executor(s.Inst)
	if len(st.Target) > 0 {
		var ok bool
		if x, ok = s.Execs[st.Target]; !ok {
			return fmt.Errorf("%s runs on target %q, which has no executor", st.Key, st.Target)
		}
	}
	if len(st.Timeout) > 0 {
		d, err := time.ParseDuration(st.Timeout)
		if err != nil {
			return fmt.Errorf("timeout %q: %w", st.Timeout, err)
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	j := s.Dir.JournalFor(s.ID)
	id := time.Now().UTC().Format("20060102T150405.000Z") + "-" + st.Name
	note := func(kind saga.Kind, msg string, e error) {
		en := saga.Entry{Time: time.Now().UTC(), Run: id, Kind: kind, Message: msg}
		if kind != saga.KindSaga && kind != saga.KindEnd {
			en.Node = st.Key
		}
		if e != nil {
			en.Error = e.Error()
		}
		_ = j.Append(en) // a journal that fails does not fail the action
	}
	note(saga.KindSaga, fmt.Sprintf("action %s on %s: %s", st.Name, st.Key, strings.Join(st.Command, " ")), nil)
	note(saga.KindStart, "action "+st.Name, nil)
	err = x.Execute(ctx, target.Step{N: st.N, Key: st.Key, Kind: string(st.Kind), Name: st.Name, Phase: st.Phase,
		Timeout: st.Timeout, Image: st.Image, Command: st.Command, Unit: u, Credentials: creds},
		func(line string) {
			fmt.Println(st.Key + ": " + line)
			note(saga.KindLog, line, nil)
		})
	if err != nil {
		note(saga.KindFail, "", err)
		note(saga.KindEnd, "failed", err)
		return fmt.Errorf("action %s on %s: %w", st.Name, st.Key, err)
	}
	note(saga.KindDone, "", nil)
	note(saga.KindEnd, "done", nil)
	return nil
}
