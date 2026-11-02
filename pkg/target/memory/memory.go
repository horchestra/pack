// Package memory is a target that runs nothing: it names the keys, keeps what it was asked to do in a log, and
// keeps the saga's record in memory. It is how the core is tested without a system underneath, and what
// `target: memory` gives an operator who wants to see a plan through to the end.
package memory

import (
	"context"
	"fmt"
	"maps"
	"sync"

	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target"
)

func init() {
	target.Register("memory", func(installation string, settings map[string]string) (target.Installation, error) {
		t := New()
		if settings["observe"] == "true" {
			t.observe = systemOf(installation)
		}
		return t, nil
	})
}

// Target is the memory target of one installation.
type Target struct {
	// Fail, when set, is asked before a step is performed; an error fails it.
	Fail func(target.Step) error

	mu     sync.Mutex
	steps  []target.Step
	record []byte
	has    bool
	// observe is the system the target runs in, when it was asked to be observable (observe: true): it outlives
	// the target, as a real system outlives the process that changed it.
	observe *system
}

type system struct {
	mu   sync.Mutex
	held map[string]target.Observed
}

var (
	systemsMu sync.Mutex
	systems   = map[string]*system{}
)

func systemOf(installation string) *system {
	systemsMu.Lock()
	defer systemsMu.Unlock()
	if systems[installation] == nil {
		systems[installation] = &system{held: map[string]target.Observed{}}
	}
	return systems[installation]
}

// New is an empty memory target.
func New() *Target { return &Target{} }

var (
	_ target.Installation = (*Target)(nil)
	_ target.Recorder     = (*Target)(nil)
	_ target.Observer     = (*Target)(nil)
)

func (*Target) Namespace() string { return "memory" }

// Resolve: a key is <key>.memory; a key the environment gives replicas has that many instances.
func (*Target) Resolve(key string, p render.Placement) (*render.Resolved, error) {
	res := &render.Resolved{Service: key, Address: key + ".memory"}
	if p.Replicas != nil {
		n := *p.Replicas
		if n < 1 {
			return nil, fmt.Errorf("replicas: %d", n)
		}
		for i := range n {
			name := fmt.Sprintf("%s-%d", key, i)
			res.Instances = append(res.Instances, render.Instance{Name: name, Address: name + "." + key + ".memory"})
		}
	}
	return res, nil
}

// Execute logs the step.
func (t *Target) Execute(ctx context.Context, s target.Step, events func(string)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.Fail != nil {
		if err := t.Fail(s); err != nil {
			return err
		}
	}
	t.mu.Lock()
	t.steps = append(t.steps, s)
	t.mu.Unlock()
	if sys := t.observe; sys != nil {
		sys.mu.Lock()
		switch s.Kind {
		case target.Apply:
			if u := s.Unit; u != nil {
				sys.held[s.Key] = target.Observed{Image: u.Image, Replicas: u.Replicas}
			}
		case target.Remove:
			delete(sys.held, s.Key)
		}
		sys.mu.Unlock()
	}
	if events != nil {
		events(s.Kind + " " + s.Key)
	}
	return nil
}

// Steps are the steps performed, in order.
func (t *Target) Steps() []target.Step {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]target.Step(nil), t.steps...)
}

func (t *Target) ReadInstalled() ([]byte, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.record...), t.has, nil
}

func (t *Target) WriteInstalled(r []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.record, t.has = append([]byte(nil), r...), true
	return nil
}

func (t *Target) DeleteInstalled() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.record, t.has = nil, false
	return nil
}

// Observe is what the steps performed left in the system: a key applied, and not removed since, with the image and
// replicas of its unit. A target not set up with `observe: true` does not tell.
func (t *Target) Observe(context.Context) (map[string]target.Observed, error) {
	sys := t.observe
	if sys == nil {
		return nil, target.ErrNotObserved
	}
	sys.mu.Lock()
	defer sys.mu.Unlock()
	return maps.Clone(sys.held), nil
}

// Lose forgets a key, as if it was removed from the system by hand.
func (t *Target) Lose(key string) {
	if sys := t.observe; sys != nil {
		sys.mu.Lock()
		defer sys.mu.Unlock()
		delete(sys.held, key)
	}
}
