package saga

import (
	"context"
	"fmt"
	"time"
)

// Kind is what an Entry records.
type Kind string

const (
	KindSaga   Kind = "saga"   // the saga starts or continues
	KindStart  Kind = "start"  // a node starts
	KindDone   Kind = "done"   // a node is in effect
	KindFail   Kind = "fail"   // a node (or the saga) failed
	KindUndo   Kind = "undo"   // a node is being undone
	KindUndone Kind = "undone" // a node is undone
	KindLog    Kind = "log"    // a line of text, of a node or of the saga
	KindEnd    Kind = "end"    // the saga ends: installed, failed, removed
)

// Entry is one line of the journal.
type Entry struct {
	Time    time.Time `json:"time"`
	Run     string    `json:"run"` // the saga's id; a continued saga keeps it
	Kind    Kind      `json:"kind"`
	Node    string    `json:"node,omitempty"`
	Step    string    `json:"step,omitempty"` // the step of the node a line is of; of an undo, undo or remove
	Message string    `json:"message,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// Line is what a person reads of an entry as the saga goes, and whether there is anything: a node applied or
// undone, a line of a node or of the saga, the saga's failure; the rest only the journal keeps.
func (e Entry) Line() (string, bool) {
	switch e.Kind {
	case KindStart:
		return "apply " + e.Node, true
	case KindUndo:
		return e.Step + " " + e.Node, true
	case KindLog:
		if len(e.Node) > 0 {
			return e.Node + ": " + e.Message, true
		}
		return e.Message, true
	case KindFail:
		if len(e.Node) == 0 && e.Message == "saga failed" {
			return "failed: " + e.Error, true
		}
	}
	return "", false
}

func (e Entry) String() string {
	s := e.Time.Local().Format("15:04:05") + " " + string(e.Kind)
	if len(e.Node) > 0 {
		s += " " + e.Node
	}
	if len(e.Message) > 0 {
		s += ": " + e.Message
	}
	if len(e.Error) > 0 {
		s += " (" + e.Error + ")"
	}
	return s
}

// Journal is the saga's history: the stages it went through and the logs of its
// nodes, append-only. The record says what is in effect now; the journal says
// how it came to be. A journal is where the operator chooses — a directory, an
// object store, a target — and need not be safe for concurrent use: the saga
// serializes the calls. A failed Append never fails the saga.
type Journal interface {
	Append(Entry) error
}

// JournalReader reads a journal back: the sagas it holds, the entries of one, and the removal of the oldest
// (Prune keeps the keep newest and says which went). A command that shows or prunes the journal asks for no more.
type JournalReader interface {
	Runs() ([]string, error)
	Read(run string) ([]Entry, error)
	Prune(keep int) ([]string, error)
}

// Logf writes a line to the saga's journal and events, as the node whose step ctx is. An Executor calls it
// for whatever it wants to be kept of a step: what it does, what the system answered. Outside a saga it does nothing.
func Logf(ctx context.Context, format string, args ...any) {
	if l, ok := ctx.Value(logKey{}).(*nodeLog); ok {
		l.r.logNode(l.node, l.step, fmt.Sprintf(format, args...))
	}
}

// WithStep is ctx of a node's step: the lines Logf writes under it say the step.
func WithStep(ctx context.Context, step string) context.Context {
	if l, ok := ctx.Value(logKey{}).(*nodeLog); ok {
		return context.WithValue(ctx, logKey{}, &nodeLog{r: l.r, node: l.node, step: step})
	}
	return ctx
}

type logKey struct{}

type nodeLog struct {
	r    *Run
	node string
	step string
}

func withNode(ctx context.Context, r *Run, node string) context.Context {
	return context.WithValue(ctx, logKey{}, &nodeLog{r: r, node: node})
}

func newRunID() string { return time.Now().UTC().Format("20060102T150405.000Z") }
