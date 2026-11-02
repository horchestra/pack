// Package saga drives an installation over the dependency graph (package graph)
// to its end: nodes run in parallel as far as the graph lets them, the record
// of what is in effect is written through a Store after every change, and a
// failure rolls back what is in effect and not committed by a savepoint.
//
// The saga knows nothing about where or how a node runs: no platform, no
// placement. A node is applied and undone by an Executor, which owns all that.
// Values (the application's configuration) are carried as an opaque document.
package saga

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/horchestra/pack/pkg/graph"
	"github.com/horchestra/pack/pkg/runner"
)

// Executor performs a node in its own system. Both calls must be safe to repeat:
// Undo of a node that was only partly applied is how a cut-short step is cleaned up.
type Executor interface {
	Apply(ctx context.Context, n *graph.Node) error
	Undo(ctx context.Context, n *graph.Node) error
}

// Store keeps the record with the target; the saga keeps no state of its own.
type Store interface {
	Load() (*Record, error) // nil, nil — nothing is installed
	Save(*Record) error
	Delete() error
}

// Locker is a Store that can refuse a second saga on the same installation. The saga takes the lock for
// Execute and for Remove, if its Store has one.
type Locker interface {
	Lock() (unlock func(), err error)
}

const (
	Installed  = "installed"
	InProgress = "in-progress"
	Failed     = "failed"
)

// Installation is one version as it was installed.
type Installation struct {
	Pack    string          `json:"pack"`
	Version string          `json:"version,omitempty"`
	Values  json.RawMessage `json:"values,omitempty"`
}

// Record is the saga's record of an installation.
type Record struct {
	// ID names the installation: a UUID v4, made at the first apply and kept for good (terraform's lineage).
	// Targets, the secret store and the journal know the installation by it.
	ID string `json:"id,omitempty"`
	Installation
	// State: installed, in-progress (a saga is running or was cut short) or
	// failed (it failed; Saga says how far it got and what was undone).
	State string    `json:"state"`
	Saga  *Progress `json:"saga,omitempty"`
	// Previous is what the last finished saga replaced.
	Previous *Installation `json:"previous,omitempty"`
}

// Progress is a saga under way, or the last one that failed.
type Progress struct {
	Installation
	// Done are the nodes in effect: applied and not undone.
	Done []string `json:"done,omitempty"`
	// Running are the nodes under way when the record was written: a saga cut
	// short there may have them partly in effect.
	Running []string `json:"running,omitempty"`
	// ID names the saga in the journal; a saga that is continued keeps it.
	ID string `json:"id,omitempty"`
	// Error names the node that failed and why.
	Error string `json:"error,omitempty"`
}

// Unfinished reports whether the record holds a saga that did not end.
func (r *Record) Unfinished() bool { return r != nil && r.State != Installed && r.Saga != nil }

// ErrInterrupted ends a saga that ctx stopped.
var ErrInterrupted = errors.New("interrupted")

// Run is one saga over a graph: install or upgrade to the node set of Graph.
type Run struct {
	Graph *graph.Graph
	Exec  Executor
	Store Store
	// ID is the installation's id; the record takes it at the first saga and refuses a saga of another.
	ID string
	// To is where the saga goes. Its Values are kept verbatim.
	To Installation
	// Jobs is how many nodes run at once (runner.Options.Jobs).
	Jobs int
	// NoRollback leaves what is in effect as it is on a failure.
	NoRollback bool
	// Keep are the nodes a rollback after a failure leaves as they are, whatever is behind a savepoint — a database
	// whose schema was migrated is not taken back by itself. Undo, the explicit take-back, ignores it.
	Keep []string
	// Events hears what happens, a line at a time; calls are serialised.
	Events func(string)
	// Journal keeps the history: the stages of the saga and the logs of its nodes. Nil — none is kept.
	Journal Journal
	// Finish runs once every node is in effect, before the record says installed: what has to be done for the
	// version to be whole (the keys it dropped are taken away). Its failure keeps the record in-progress, with all
	// the nodes done: the same apply runs it again. It runs under the saga's lock; it must not take it.
	Finish func(ctx context.Context) error

	mu  sync.Mutex
	rec *Record

	jmu   sync.Mutex // the journal's calls are serialised
	jwarn bool
	runID string
}

func (r *Run) say(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	r.note(KindLog, "", msg, nil)
	r.event(msg)
}

func (r *Run) event(msg string) {
	if r.Events != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.Events(msg)
	}
}

// logNode is a line of a node: heard, and kept.
func (r *Run) logNode(node, msg string) {
	r.note(KindLog, node, msg, nil)
	r.event(node + ": " + msg)
}

// note appends to the journal. A journal that fails is said once and then left alone: the record, not the
// journal, is what the saga stands on.
func (r *Run) note(kind Kind, node, msg string, err error) {
	if r.Journal == nil {
		return
	}
	e := Entry{Time: time.Now().UTC(), Run: r.runID, Kind: kind, Node: node, Message: msg}
	if err != nil {
		e.Error = err.Error()
	}
	r.jmu.Lock()
	defer r.jmu.Unlock()
	if jerr := r.Journal.Append(e); jerr != nil && !r.jwarn {
		r.jwarn = true
		go r.event("journal: " + jerr.Error() + " — the saga goes on without it")
	}
}

// save writes the record; callers hold r.mu.
func (r *Run) save() error { return r.Store.Save(r.rec) }

// Execute installs. A saga the record shows unfinished continues: the nodes
// in effect stay, the ones that were under way are undone and run again.
// A failure rolls back every node in effect that no savepoint commits; the
// record then says what failed and what is still in effect.
func (r *Run) Execute(ctx context.Context) error {
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	rec, err := r.Store.Load()
	if err != nil {
		return fmt.Errorf("the installation's record: %w", err)
	}
	if rec == nil {
		rec = &Record{}
	}
	if len(r.ID) > 0 {
		if len(rec.ID) > 0 && rec.ID != r.ID {
			return fmt.Errorf("the record is of installation %s, this is %s", rec.ID, r.ID)
		}
		rec.ID = r.ID
	}
	r.rec = rec
	if !rec.Unfinished() || rec.Saga.Pack != r.To.Pack || rec.Saga.Version != r.To.Version || !sameJSON(rec.Saga.Values, r.To.Values) {
		if rec.Unfinished() {
			return fmt.Errorf("an unfinished saga towards %s %s is in the record: continue it with the same pack and values, or take it back", rec.Saga.Pack, rec.Saga.Version)
		}
		rec.Saga = &Progress{Installation: r.To}
	}
	sg := rec.Saga
	if len(sg.ID) == 0 {
		sg.ID = newRunID()
	}
	r.runID = sg.ID
	r.note(KindSaga, "", fmt.Sprintf("towards %s %s", sg.Pack, sg.Version), nil)

	// what was cut short is undone before anything runs again
	for _, name := range slices.Clone(sg.Running) {
		n := r.Graph.Node(name)
		if n == nil {
			return fmt.Errorf("the record has node %q under way, the pack has none", name)
		}
		r.say("undo cut-short %s", name)
		if err := r.Exec.Undo(withNode(ctx, r, name), n); err != nil {
			return fmt.Errorf("undo %s: %w", name, err)
		}
		sg.Running = remove(sg.Running, name)
	}
	rec.State, sg.Error = InProgress, ""
	if err := r.save(); err != nil {
		return fmt.Errorf("record the saga: %w", err)
	}

	res, runErr := runner.Run(ctx, r.Graph, r.forward, runner.Options{Jobs: r.Jobs, FailFast: true})
	if runErr == nil && res.Err() == nil {
		if r.Finish != nil {
			if err := r.Finish(ctx); err != nil {
				r.note(KindFail, "", "finish failed", err)
				return fmt.Errorf("%w — every node is in effect, the record stays in progress: run it again", err)
			}
		}
		return r.finish()
	}
	return r.fail(ctx, res, runErr)
}

// forward is the runner of the graph: a node in effect is passed over.
func (r *Run) forward(ctx context.Context, n *graph.Node) error {
	r.mu.Lock()
	if slices.Contains(r.rec.Saga.Done, n.Name) {
		r.mu.Unlock()
		return nil
	}
	// a node whose start is not written must not run
	r.rec.Saga.Running = append(r.rec.Saga.Running, n.Name)
	err := r.save()
	r.mu.Unlock()
	if err != nil {
		return fmt.Errorf("record %s: %w", n.Name, err)
	}
	r.note(KindStart, n.Name, "", nil)
	r.event("apply " + n.Name)
	if err := r.Exec.Apply(withNode(ctx, r, n.Name), n); err != nil {
		r.note(KindFail, n.Name, "", err)
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.Saga.Running = remove(r.rec.Saga.Running, n.Name)
	r.rec.Saga.Done = append(r.rec.Saga.Done, n.Name)
	if err := r.save(); err != nil {
		return fmt.Errorf("record %s: %w", n.Name, err)
	}
	r.note(KindDone, n.Name, "", nil)
	return nil
}

func (r *Run) finish() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, sg := r.rec, r.rec.Saga
	if len(rec.Pack) > 0 && (rec.Pack != sg.Pack || rec.Version != sg.Version || !sameJSON(rec.Values, sg.Values)) {
		prev := rec.Installation
		rec.Previous = &prev
	}
	rec.Installation, rec.State, rec.Saga = sg.Installation, Installed, nil
	err := r.save()
	r.note(KindEnd, "", "installed "+rec.Pack+" "+rec.Version, err)
	return err
}

// fail records the failure and rolls back to the last savepoint.
func (r *Run) fail(ctx context.Context, res *runner.Result, runErr error) error {
	cause := res.Err()
	if cause == nil {
		cause = ErrInterrupted
	}
	r.mu.Lock()
	r.rec.State, r.rec.Saga.Error = Failed, cause.Error()
	werr := r.save()
	r.mu.Unlock()
	if werr != nil {
		return fmt.Errorf("%w; record the failure: %w", cause, werr)
	}
	r.note(KindFail, "", "saga failed", cause)
	r.event(fmt.Sprintf("failed: %v", cause))
	if r.NoRollback {
		return cause
	}
	// the rollback runs on a context of its own: ctx done is why we are here
	rbctx := ctx
	if runErr != nil {
		var cancel context.CancelFunc
		rbctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
	}
	if err := r.rollback(rbctx); err != nil {
		return fmt.Errorf("%w; rollback: %w — the record keeps what is in effect", cause, err)
	}
	return cause
}

// reverse undoes nodes, dependents first: every node mine accepts (nil — all
// of them), calling after (nil — nothing) once the node is undone.
func (r *Run) reverse(ctx context.Context, verb string, mine func(*graph.Node) bool, after func(*graph.Node) error) error {
	res, err := runner.Reverse(ctx, r.Graph, func(ctx context.Context, n *graph.Node) error {
		if mine != nil && !mine(n) {
			return nil
		}
		r.note(KindUndo, n.Name, "", nil)
		r.event(verb + " " + n.Name)
		if err := r.Exec.Undo(withNode(ctx, r, n.Name), n); err != nil {
			r.note(KindFail, n.Name, verb, err)
			return err
		}
		if after != nil {
			if err := after(n); err != nil {
				return err
			}
		}
		r.note(KindUndone, n.Name, "", nil)
		return nil
	}, runner.Options{Jobs: r.Jobs, FailFast: true})
	return errors.Join(res.Err(), err)
}

// forget drops the node from the saga's record of what is in effect.
func (r *Run) forget(n *graph.Node) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.Saga.Done = remove(r.rec.Saga.Done, n.Name)
	r.rec.Saga.Running = remove(r.rec.Saga.Running, n.Name)
	return r.save()
}

// rollback undoes, last first, every node in effect or under way that no savepoint commits.
func (r *Run) rollback(ctx context.Context) error {
	// the set of what is in effect is taken once, under the lock: forget drops
	// from it as the rollback runs, and reverse asks mine from other goroutines
	r.mu.Lock()
	committed := r.Graph.Committed(func(n *graph.Node) bool { return slices.Contains(r.rec.Saga.Done, n.Name) })
	inEffect := map[string]bool{}
	for _, name := range r.rec.Saga.Done {
		inEffect[name] = true
	}
	for _, name := range r.rec.Saga.Running {
		inEffect[name] = true
	}
	r.mu.Unlock()
	mine := func(n *graph.Node) bool {
		return !committed[n] && !slices.Contains(r.Keep, n.Name) && inEffect[n.Name]
	}
	return r.reverse(ctx, "undo", mine, r.forget)
}

// Undo takes an unfinished saga back: every node of it in effect or under way is undone, last first, whatever
// the savepoints — the part a failed saga leaves behind them too. The record then says the version installed
// before the saga is installed, as it was, or, for a first installation, that nothing is: the record goes.
// Graph is the graph of the version the saga went to; Exec's Undo returns a node to the version it replaced.
func (r *Run) Undo(ctx context.Context) error {
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	rec, err := r.Store.Load()
	if err != nil {
		return fmt.Errorf("the installation's record: %w", err)
	}
	if !rec.Unfinished() {
		return fmt.Errorf("no unfinished saga to undo")
	}
	r.rec = rec
	sg := rec.Saga
	if len(sg.ID) == 0 {
		sg.ID = newRunID()
	}
	r.runID = sg.ID
	r.note(KindSaga, "", fmt.Sprintf("undo the saga towards %s %s", sg.Pack, sg.Version), nil)
	// a savepoint is a node that does nothing; the graph of an undo, built without the plan, may not have it
	sg.Done = slices.DeleteFunc(sg.Done, graph.IsSavepoint)
	sg.Running = slices.DeleteFunc(sg.Running, graph.IsSavepoint)
	for _, name := range append(slices.Clone(sg.Done), sg.Running...) {
		if r.Graph.Node(name) == nil {
			return fmt.Errorf("the record has node %q in effect, the pack has none: the record does not match it", name)
		}
	}
	if err := r.reverse(ctx, "undo", func(n *graph.Node) bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return slices.Contains(sg.Done, n.Name) || slices.Contains(sg.Running, n.Name)
	}, r.forget); err != nil {
		r.note(KindFail, "", "undo failed", err)
		return fmt.Errorf("%w — the record keeps what is still in effect: undo again", err)
	}
	if len(rec.Pack) == 0 { // a first installation taken back: nothing is installed
		err = r.clear(rec.ID)
		r.note(KindEnd, "", "nothing is installed", err)
		return err
	}
	rec.State, rec.Saga = Installed, nil
	err = r.save()
	r.note(KindEnd, "", "back to "+rec.Pack+" "+rec.Version, err)
	return err
}

// Remove uninstalls: every node is undone, dependents first, and the record goes.
func (r *Run) Remove(ctx context.Context) error {
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := r.removeNodes(ctx); err != nil {
		return err
	}
	rec, err := r.Store.Load()
	if err != nil {
		return fmt.Errorf("the installation's record: %w", err)
	}
	var id string
	if rec != nil {
		id = rec.ID
	}
	err = r.clear(id)
	r.note(KindEnd, "", "removed", err)
	return err
}

// clear takes the installation off the record but keeps its id: the passwords and objects the installation
// made are known by it, and what stays after a removal must be found again by the next install.
func (r *Run) clear(id string) error {
	if len(id) == 0 {
		return r.Store.Delete()
	}
	return r.Store.Save(&Record{ID: id})
}

// RemoveNodes undoes every node of the graph, dependents first, and leaves the record: what an upgrade does
// with the keys the new version no longer has.
func (r *Run) RemoveNodes(ctx context.Context) error {
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return r.removeNodes(ctx)
}

func (r *Run) removeNodes(ctx context.Context) error {
	r.runID = newRunID()
	r.note(KindSaga, "", "remove", nil)
	if err := r.reverse(ctx, "remove", nil, nil); err != nil {
		r.note(KindFail, "", "remove failed", err)
		return err
	}
	return nil
}

func (r *Run) lock() (func(), error) {
	if l, ok := r.Store.(Locker); ok {
		return l.Lock()
	}
	return func() {}, nil
}

func remove(s []string, name string) []string {
	return slices.DeleteFunc(s, func(x string) bool { return x == name })
}

func sameJSON(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}
