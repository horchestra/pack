// Package runner walks a graph.Graph: a node runs when everything it waits for is done,
// up to Jobs at once. It knows the graph's shape and nothing of what a node is.
package runner

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"

	"github.com/horchestra/pack/internal/graph"
)

// Status is where a node ended up.
type Status string

const (
	Pending  Status = "pending"  // never reached
	Running  Status = "running"  // event only
	Done     Status = "done"     // the runner returned nil
	Failed   Status = "failed"   // the runner returned an error
	Skipped  Status = "skipped"  // something it waits for failed or was skipped
	Canceled Status = "canceled" // not started: ctx done, or FailFast after a failure
)

// Event is one change of a node's status. Events are delivered one at a time,
// from the goroutine that calls Run, so a handler needs no lock.
type Event struct {
	Node   *graph.Node
	Status Status
	Err    error // for Failed
}

// Func does the work of one node. It is called from its own goroutine, once
// the nodes the node waits for are Done.
type Func func(ctx context.Context, n *graph.Node) error

// Options tune a run.
type Options struct {
	// Jobs is how many nodes run at once; 0 — the number of CPUs, 1 — one by one in Order.
	Jobs int
	// FailFast: the first failure cancels the running nodes (their ctx) and
	// starts nothing else. Without it, only the nodes that wait for a failed
	// one are skipped; independent branches go on.
	FailFast bool
	// Events hears every change of status; nil — nobody.
	Events func(Event)
}

// Result is the outcome of a run.
type Result struct {
	Status map[*graph.Node]Status
	Errors map[*graph.Node]error // for Failed
}

// Err joins the errors of the failed nodes, nil when none failed. Safe on a nil Result.
func (r *Result) Err() error {
	if r == nil {
		return nil
	}
	var errs []error
	for n, e := range r.Errors {
		errs = append(errs, fmt.Errorf("%s: %w", n.Name, e))
	}
	return errors.Join(errs...)
}

// Run walks the graph: a node starts when everything it waits for is Done, up
// to Options.Jobs at once. The Result is never nil, whatever the error; the
// error is that of ctx, if it ended the run, and failures of nodes are in the Result.
func Run(ctx context.Context, g *graph.Graph, run Func, opt Options) (*Result, error) {
	return walk(ctx, run, opt, g.Deps, g.Dependents, g.Order())
}

// Reverse walks the other way — a node starts when everything that waits
// for it has ended: what a teardown follows.
func Reverse(ctx context.Context, g *graph.Graph, run Func, opt Options) (*Result, error) {
	return walk(ctx, run, opt, g.Dependents, g.Deps, g.Reverse())
}

// walk: before(n) are the nodes that must be Done first, after(n) the ones n releases.
func walk(ctx context.Context, run Func, opt Options, before, after func(*graph.Node) []*graph.Node, order []*graph.Node) (*Result, error) {
	jobs := opt.Jobs
	if jobs <= 0 {
		jobs = runtime.NumCPU()
	}
	res := &Result{Status: map[*graph.Node]Status{}, Errors: map[*graph.Node]error{}}
	emit := func(n *graph.Node, s Status, err error) {
		res.Status[n] = s
		if opt.Events != nil {
			opt.Events(Event{Node: n, Status: s, Err: err})
		}
	}
	wait := map[*graph.Node]int{}
	var ready []*graph.Node // in Order, so that Jobs=1 is Order
	for _, n := range order {
		res.Status[n] = Pending
		wait[n] = len(before(n))
		if wait[n] == 0 {
			ready = append(ready, n)
		}
	}
	wctx, stop := context.WithCancel(ctx)
	defer stop()
	type result struct {
		n   *graph.Node
		err error
	}
	results := make(chan result)
	running, halted := 0, false

	// skip marks everything that waits, transitively, for n.
	var skip func(n *graph.Node)
	skip = func(n *graph.Node) {
		for _, m := range after(n) {
			if res.Status[m] == Pending {
				emit(m, Skipped, nil)
				skip(m)
			}
		}
	}
	launch := func() {
		for !halted && running < jobs && len(ready) > 0 {
			if wctx.Err() != nil {
				halted = true
				break
			}
			// a savepoint costs nothing: it goes before whatever else is waiting
			i := max(slices.IndexFunc(ready, func(n *graph.Node) bool { return n.Savepoint }), 0)
			n := ready[i]
			ready = slices.Delete(ready, i, i+1)
			running++
			emit(n, Running, nil)
			go func() { results <- result{n, run(wctx, n)} }()
		}
	}
	launch()
	for running > 0 {
		r := <-results
		running--
		if r.err == nil {
			emit(r.n, Done, nil)
			for _, m := range after(r.n) {
				if wait[m]--; wait[m] == 0 && res.Status[m] == Pending {
					ready = append(ready, m)
				}
			}
		} else {
			res.Errors[r.n] = r.err
			emit(r.n, Failed, r.err)
			skip(r.n)
			if opt.FailFast {
				halted = true
				stop()
			}
		}
		launch()
	}
	for _, n := range order {
		if res.Status[n] == Pending {
			emit(n, Canceled, nil)
		}
	}
	return res, ctx.Err()
}
