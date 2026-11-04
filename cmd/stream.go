package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/saga"
)

// --stream: what a command does, as it does it, is NDJSON on stdout — one event a line — and nothing else is there:
// the text a person reads goes to stderr. What a UI's backend forwards as it comes (an SSE `data:`, a WebSocket
// frame) without reading text. The last event is the result.
var streamOut bool

// The types of an event.
const (
	EventPlan   = "plan"   // the plan the command runs: Plan
	EventNode   = "node"   // a key changes status: Status
	EventLog    = "log"    // a line of a key (a step of it) or of the saga
	EventSaga   = "saga"   // the saga starts, ends, fails, is interrupted
	EventResult = "result" // the last: ok, and the command's result (Result) — or failed, and the error
)

// The statuses of a key, and of a result.
const (
	StatusRunning     = "running"
	StatusDone        = "done"
	StatusFailed      = "failed"
	StatusUndoing     = "compensating" // a rollback, or an undo, takes it back
	StatusRolledBack  = "rolled-back"
	StatusRemoving    = "removing"
	StatusRemoved     = "removed"
	StatusStarted     = "started"
	StatusEnded       = "ended"
	StatusInterrupted = "interrupted"
	StatusOK          = "ok"
)

// Event is a line of --stream.
type Event struct {
	// Seq is 1, 2, … in the order the events are written: an SSE id, what Last-Event-ID resumes from.
	Seq     int64     `json:"seq"`
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Key     string    `json:"key,omitempty"`
	Step    string    `json:"step,omitempty"`
	Status  string    `json:"status,omitempty"`
	Message string    `json:"message,omitempty"`
	Error   string    `json:"error,omitempty"`
	// Saga is the id of the saga an event is of (pack log <it>).
	Saga string `json:"saga,omitempty"`

	Plan   *plan.Plan `json:"plan,omitempty"`
	Result any        `json:"result,omitempty"`
	Code   string     `json:"code,omitempty"`
	Next   []string   `json:"next,omitempty"`
}

var stream struct {
	mu   sync.Mutex
	seq  int64
	enc  *json.Encoder
	saga string // the saga the last event was of: what an interrupt is of
}

func init() { streamFlag(applyCmd, planCmd, deleteCmd, rollbackCmd, logCmd) }

// streamFlag gives commands --stream.
func streamFlag(cmds ...*cobra.Command) {
	for _, c := range cmds {
		c.Flags().BoolVar(&streamOut, "stream", false, "what is done, as it is done, as NDJSON on stdout — one event a line, the result last; the text on stderr (docs/commands.md#--stream)")
	}
}

// startStream makes stdout the events' alone, the text going to stderr as with --json; the first interrupt is an
// event too.
func startStream() error {
	if jsonOut {
		return errors.New("--stream or --json, not both: the stream's last event is the result --json writes")
	}
	asJSON()
	// a reader that went away is an error of a write, not a SIGPIPE that ends pack in the middle of a saga
	signal.Ignore(syscall.SIGPIPE)
	stream.mu.Lock()
	stream.seq, stream.enc, stream.saga = 0, json.NewEncoder(stdout), ""
	stream.mu.Unlock()
	interrupt.Heard = func(os.Signal) {
		stream.mu.Lock()
		id := stream.saga
		stream.mu.Unlock()
		streamEvent(Event{Type: EventSaga, Status: StatusInterrupted, Saga: id, Message: "interrupted: the running steps are cut short, the saga rolls back"})
	}
	return nil
}

// stopStream is the end of a command's stream: the next command of a process (a test's) starts without one.
func stopStream() {
	interrupt.Heard = nil
	signal.Reset(syscall.SIGPIPE)
	if f, ok := stdout.(*os.File); ok {
		os.Stdout = f
	}
	streamOut = false
}

// streamEvent writes an event, its seq and time given; nothing without --stream.
func streamEvent(e Event) {
	if !streamOut {
		return
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.enc == nil {
		return
	}
	stream.seq++
	e.Seq = stream.seq
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	if len(e.Saga) > 0 {
		stream.saga = e.Saga
	}
	if err := stream.enc.Encode(e); err != nil {
		// the reader is gone: the command goes on — a saga to its end or its rollback — and says it on stderr
		stream.enc = nil
		_, _ = fmt.Fprintf(os.Stderr, "pack: --stream: %v — the events stop, the command goes on\n", err)
	}
}

// planned is the plan a command runs, as an event.
func planned(p *plan.Plan) { streamEvent(Event{Type: EventPlan, Plan: p}) }

// entryEvent is an entry of a saga's journal as an event: the same as the saga runs and as pack log reads it back.
func entryEvent(e saga.Entry) Event {
	ev := Event{Time: e.Time, Key: e.Node, Step: e.Step, Message: e.Message, Error: e.Error, Saga: e.Run}
	switch e.Kind {
	case saga.KindStart:
		ev.Type, ev.Status = EventNode, StatusRunning
	case saga.KindDone:
		ev.Type, ev.Status = EventNode, StatusDone
	case saga.KindUndo:
		ev.Type, ev.Status, ev.Step = EventNode, StatusUndoing, "" // its step is the verb: the status says it
		if e.Step == "remove" {
			ev.Status = StatusRemoving
		}
	case saga.KindUndone:
		ev.Type, ev.Status, ev.Step = EventNode, StatusRolledBack, ""
		if e.Step == "remove" {
			ev.Status = StatusRemoved
		}
	case saga.KindFail:
		ev.Type, ev.Status = EventNode, StatusFailed
		if len(e.Node) == 0 {
			ev.Type = EventSaga
		}
	case saga.KindSaga:
		ev.Type, ev.Status = EventSaga, StatusStarted
	case saga.KindEnd:
		ev.Type, ev.Status = EventSaga, StatusEnded
		if len(e.Error) > 0 {
			ev.Status = StatusFailed
		}
	default:
		ev.Type = EventLog
	}
	return ev
}
