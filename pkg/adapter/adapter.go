// Package adapter connects the new saga to what pack already has for talking
// to a target: target.Executor performs one step in its system, the rendered
// Release says what a key is, plan says which steps a key takes.
//
// The saga hands this package a node; it turns it into the target's steps.
// Placement stays inside the target: nothing here reads or keeps a platform.
package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/horchestra/pack/pkg/graph"
	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/target"
)

// Store keeps the saga's record with the target, as JSON.
type Store struct{ X target.Recorder }

func (s Store) Load() (*saga.Record, error) {
	raw, exists, err := s.X.ReadInstalled()
	if err != nil || !exists {
		return nil, err
	}
	var r saga.Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("the installation's record: %w", err)
	}
	return &r, nil
}

func (s Store) Save(r *saga.Record) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.X.WriteInstalled(raw)
}

func (s Store) Delete() error { return s.X.DeleteInstalled() }

// Specs are the nodes of a rendered release for graph.New: its keys, and what each waits for (Report.Deps, the
// keys that are in the release). Every unit of the plan that names a savepoint adds a node of its own,
// `savepoint:<name>`, that waits for all the keys of the unit: when it is done, the unit and what it waits for stay
// installed through a later failure. A savepoint does nothing, so it runs as soon as it can. A nil plan has none.
func Specs(rel *render.Release, p *plan.Plan) []graph.Spec {
	in := map[string]bool{}
	for _, u := range rel.Units {
		in[u.Key] = true
	}
	var specs []graph.Spec
	for _, u := range rel.Units {
		s := graph.Spec{Name: u.Key}
		for _, d := range rel.Report.Deps[u.Key] {
			if in[d] {
				s.Deps = append(s.Deps, d)
			}
		}
		specs = append(specs, s)
	}
	if p != nil {
		for _, u := range p.Units {
			if len(u.Savepoint) == 0 {
				continue
			}
			sp := graph.Spec{Name: graph.SavepointPrefix + u.Savepoint, Savepoint: true}
			for _, k := range u.Keys {
				if in[k] {
					sp.Deps = append(sp.Deps, k)
				}
			}
			specs = append(specs, sp)
		}
	}
	return specs
}

// Exec performs a node on the target. Apply runs the steps the plan has for its key. Undo is what takes them
// back: the compensation of each hook that ran, the downgrade hooks around applying the unit of the version being
// left, and the removal of a key that was added. A key the plan did not touch is left alone.
type Exec struct {
	// Plan holds the steps; only those of the node's key are run, in plan order.
	// Nil — an uninstall, or the undo of a saga whose plan is not at hand: Undo works from the units alone.
	Plan        *plan.Plan
	From, To    *render.Release // the installed version (nil on a first installation) and the one going in (nil on a delete)
	Credentials []render.Credential
	X           target.Executor
	Execs       map[string]target.Executor // other targets by name; a step runs on the one its plan step names

	mu        sync.Mutex
	performed map[string]map[int]bool // key → plan steps begun in this process
}

// Apply runs the key's steps.
func (e *Exec) Apply(ctx context.Context, n *graph.Node) error {
	if e.Plan == nil {
		return fmt.Errorf("%s: no plan to apply", n.Name)
	}
	for _, s := range e.Plan.Steps {
		if s.Key != n.Name || s.Kind == plan.Remove {
			continue
		}
		e.begin(n.Name, s.N) // the step under way counts: it may be partly in effect
		saga.Logf(ctx, "%s %s", s.Kind, s.Name)
		if err := e.run(ctx, s.Target, e.forward(s)); err != nil {
			return fmt.Errorf("%s %s: %w", s.Kind, n.Name, err)
		}
	}
	return nil
}

func (e *Exec) begin(key string, n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.performed == nil {
		e.performed = map[string]map[int]bool{}
	}
	if e.performed[key] == nil {
		e.performed[key] = map[int]bool{}
	}
	e.performed[key][n] = true
}

// Undo takes the key back. It must be safe to repeat: a saga cut short undoes what it cannot tell was done.
func (e *Exec) Undo(ctx context.Context, n *graph.Node) error {
	to, prev := unit(e.To, n.Name), unit(e.From, n.Name)
	if e.To != nil && prev != nil && reflect.DeepEqual(prev, to) {
		return nil // the version being left has the key as it is: nothing of it changed
	}
	steps := e.keySteps(n.Name)
	if e.Plan != nil && len(steps) == 0 {
		return nil // the plan did not touch the key, or none of its steps was begun
	}
	if len(steps) == 0 { // no plan: the unit alone says what to do
		st := e.base(n.Name, to, prev)
		if prev != nil && e.To != nil {
			st.Kind, st.Unit = string(plan.Apply), prev
		} else {
			st.Kind, st.Unit, st.Previous = string(plan.Remove), nil, cmpOr(to, prev)
		}
		return e.undoStep(ctx, "", st)
	}
	applied := slices.ContainsFunc(steps, func(s plan.Step) bool { return s.Kind == plan.Apply })
	for _, s := range slices.Backward(steps) {
		for _, st := range e.undoOf(s, applied, to, prev) {
			if err := e.undoStep(ctx, s.Target, st); err != nil {
				return err
			}
		}
	}
	return nil
}

// keySteps are the plan steps of the key that were begun here — or, in a process that did not run them, all.
func (e *Exec) keySteps(key string) []plan.Step {
	if e.Plan == nil {
		return nil
	}
	e.mu.Lock()
	done, known := e.performed[key]
	e.mu.Unlock()
	var out []plan.Step
	for _, s := range e.Plan.Steps {
		if s.Key == key && (!known || done[s.N]) {
			out = append(out, s)
		}
	}
	return out
}

func (e *Exec) base(key string, to, prev *render.Unit) target.Step {
	return target.Step{Key: key, Rollback: true, Unit: to, Previous: prev, Credentials: e.credentials(key)}
}

func (e *Exec) undoStep(ctx context.Context, targetName string, st target.Step) error {
	saga.Logf(ctx, "undo %s %s", st.Kind, st.Name)
	if err := e.run(ctx, targetName, st); err != nil {
		return fmt.Errorf("undo %s %s: %w", st.Kind, st.Key, err)
	}
	return nil
}

// undoOf is what undoes one plan step: nothing, a compensation, or — for the apply of a key that changed
// version — the plan's downgrade hooks around applying the previous unit.
func (e *Exec) undoOf(s plan.Step, applied bool, to, prev *render.Unit) []target.Step {
	base := e.base(s.Key, to, prev)
	base.N, base.Timeout = s.N, s.Timeout
	switch s.Kind {
	case plan.Hook:
		if len(s.CompensateCommand) == 0 {
			return nil
		}
		base.Kind, base.Name, base.Phase = string(plan.Hook), s.Name, s.Phase
		base.Image, base.Command = s.CompensateImage, s.CompensateCommand
		return []target.Step{base}
	case plan.Apply:
		if prev == nil {
			base.Kind, base.Unit = string(plan.Remove), nil // the key was added: it goes
			base.Previous = to
			return []target.Step{base}
		}
		apply := base
		apply.Kind, apply.Unit, apply.Previous = string(plan.Apply), prev, to
		var kr plan.KeyRollback
		if e.Plan != nil {
			kr = e.Plan.Rollback[s.Key]
		}
		hook := func(h plan.Step) target.Step {
			st := base
			st.Kind, st.Name, st.Phase, st.Timeout, st.Exclusive = string(plan.Hook), h.Name, h.Phase, h.Timeout, h.Exclusive
			st.Image, st.Command, st.Unit, st.Previous = h.Image, h.Command, prev, to
			return st
		}
		var out []target.Step
		if kr.Stop {
			stop := base
			stop.Kind, stop.Unit, stop.Previous = string(plan.Stop), to, prev
			out = append(out, stop)
		}
		for _, h := range kr.Pre {
			out = append(out, hook(h))
		}
		out = append(out, apply)
		for _, h := range kr.Post {
			out = append(out, hook(h))
		}
		return out
	case plan.Remove:
		if prev == nil {
			return nil
		}
		base.Kind, base.Unit = string(plan.Apply), prev
		return []target.Step{base}
	case plan.Stop:
		if applied || prev == nil {
			return nil // the apply's own undo starts it again
		}
		base.Kind, base.Unit, base.Previous = string(plan.Apply), prev, to
		return []target.Step{base}
	}
	return nil
}

func cmpOr(a, b *render.Unit) *render.Unit {
	if a != nil {
		return a
	}
	return b
}

func (e *Exec) forward(s plan.Step) target.Step {
	return target.Step{N: s.N, Key: s.Key, Kind: string(s.Kind), Name: s.Name, Phase: s.Phase, Timeout: s.Timeout,
		Exclusive: s.Exclusive, Image: s.Image, Command: s.Command,
		Unit: unit(e.To, s.Key), Previous: unit(e.From, s.Key), Credentials: e.credentials(s.Key)}
}

func (e *Exec) run(ctx context.Context, targetName string, st target.Step) error {
	if len(st.Timeout) > 0 {
		d, err := time.ParseDuration(st.Timeout)
		if err != nil {
			return fmt.Errorf("timeout %q: %w", st.Timeout, err)
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	x := e.X
	if len(targetName) > 0 {
		var ok bool
		if x, ok = e.Execs[targetName]; !ok {
			return fmt.Errorf("%s runs on target %q, which has no executor", st.Key, targetName)
		}
	}
	return x.Execute(ctx, st, func(msg string) { saga.Logf(ctx, "%s", msg) })
}

func (e *Exec) credentials(key string) []render.Credential {
	var out []render.Credential
	for _, c := range e.Credentials {
		if c.Key == key {
			out = append(out, c)
		}
	}
	return out
}

func unit(rel *render.Release, key string) *render.Unit {
	if rel == nil {
		return nil
	}
	if i := slices.IndexFunc(rel.Units, func(u render.Unit) bool { return u.Key == key }); i >= 0 {
		return &rel.Units[i]
	}
	return nil
}
