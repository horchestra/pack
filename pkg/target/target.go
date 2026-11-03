// Package target is what the core asks of the system a pack runs on, and nothing else: it names the key (Resolve,
// for the render — a neighbour's address ends up in a config file) and performs one step of a saga (Execute).
// Where a key is placed and how it is run is the environment's (render.Placement); a target is told, and says
// what that means in its system. The core knows no target by name: targets register themselves.
package target

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/horchestra/pack/pkg/render"
)

// Installation is a target bound to one installation.
type Installation interface {
	render.Target
	Executor
}

// Executor performs the steps of a saga in the target's system.
type Executor interface {
	// Execute performs one step, telling events what it does; nil — done. It must be safe to repeat: a saga cut
	// short repeats what it cannot tell was done.
	Execute(ctx context.Context, step Step, events func(string)) error
}

// Recorder is a target that can keep the saga's record in its own system (release.yaml: state: target).
type Recorder interface {
	ReadInstalled() (record []byte, exists bool, err error)
	WriteInstalled(record []byte) error
	DeleteInstalled() error
}

// Observed is a key as the target holds it.
type Observed struct {
	Image    string
	Replicas int
}

// ErrNotObserved is what Observe says when the target cannot tell what it holds just now (it was not set up to).
var ErrNotObserved = errors.New("the target does not tell what it holds")

// Observer is a target that can say what it holds of the installation — the keys, by what they run — so that a
// change made behind pack's back is seen (drift). A target that cannot is not asked.
type Observer interface {
	Observe(ctx context.Context) (map[string]Observed, error)
}

// Step kinds.
const (
	Stop   = "stop"
	Hook   = "hook"
	Apply  = "apply"
	Wait   = "wait"
	Remove = "remove"
)

// Step is one step of a saga as a target performs it: the plan's step, and what it acts on — the key's unit in the
// version being installed and in the installed one, the credentials provisioned for the key.
type Step struct {
	N         int
	Key       string
	Kind      string // stop | hook | apply | wait | remove
	Name      string // a hook's name
	Phase     string
	Timeout   string
	Exclusive bool
	Image     string   // a hook's image
	Command   []string // a hook's command
	// Rollback: the step undoes an earlier one — an apply of Previous, a remove of a key that was added, a
	// hook's compensation.
	Rollback    bool
	Unit        *render.Unit
	Previous    *render.Unit
	Credentials []render.Credential
}

// Factory makes the target for an installation from the environment's settings for it.
type Factory func(installation string, settings map[string]string) (Installation, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register makes a target known by name; a second registration of a name is a mistake in the program.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[name]; dup {
		panic("target " + name + " is registered twice")
	}
	factories[name] = f
}

// Open makes the target of that name for an installation.
func Open(name, installation string, settings map[string]string) (Installation, error) {
	mu.RLock()
	f, ok := factories[name]
	mu.RUnlock()
	if !ok {
		if Fallback != nil {
			return Fallback(name, installation, settings)
		}
		return nil, fmt.Errorf("target %q is not known (known: %v)", name, Names())
	}
	return f(installation, settings)
}

// Error is what a target said: it refused a step, or could not be started or reached. Its text is the target's own.
type Error struct{ Err error }

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// ErrNotBrought is a target whose program or image the release has not brought (pack refresh does).
var ErrNotBrought = errors.New("not brought: run pack refresh")

// Fallback opens a target that is not registered: set by the package that runs targets as programs of their own
// (target/remote), which a build of pack imports. Its error says where it looked.
var Fallback func(name, installation string, settings map[string]string) (Installation, error)

// Names are the registered targets.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(factories))
	for n := range factories {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Settings are the environment's set for a target, key=value, keys in lower case: the default target's, or the
// entries of one of `targets`.
type Settings map[string]string

// Own are the keys of the set that are pack's own, not a target's.
var Own = []string{"target", "vault_addr", "vault_paths"}

// Check refuses a setting the target does not know: every key must be pack's own, one of names, or start with one
// of prefixes. A setting nothing reads is a mistake — a typo, a setting of another target — never something to
// pass over.
func (s Settings) Check(target string, names, prefixes []string) error {
	var unknown []string
	for k := range s {
		if slices.Contains(Own, k) || slices.Contains(names, k) ||
			slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(k, p) }) {
			continue
		}
		unknown = append(unknown, k)
	}
	if len(unknown) == 0 {
		return nil
	}
	slices.Sort(unknown)
	has := slices.Clone(names)
	for _, p := range prefixes {
		has = append(has, p+"…")
	}
	return fmt.Errorf("the %s target has no setting %s; it has %s", target, strings.Join(unknown, ", "), strings.Join(has, ", "))
}

// Get returns a setting, or def when it is absent.
func (s Settings) Get(key, def string) string {
	if v, ok := s[key]; ok {
		return v
	}
	return def
}
