// Package api is the documents pack writes for a program to read: what --json writes (a command's result, or the
// error of one that failed: Failure, its codes) and the events of --stream (Event). A UI's backend runs pack and
// reads these; the types are its as much as pack's.
package api

import (
	"time"

	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/saga"
)

// The codes of an error, the closed list a reader of --json acts on.
const (
	CodeNoRelease        = "no_release"        // no release.yaml, or it names no pack: pack init
	CodeNotRefreshed     = "not_refreshed"     // the pack or a target is not brought into .pack: pack refresh
	CodeValuesMissing    = "values_missing"    // settings the schema requires are not given
	CodeValuesInvalid    = "values_invalid"    // the schema refuses a value, a rule of the pack does not hold, the environment's resources fall short
	CodeCheckFailed      = "check_failed"      // pack check found problems
	CodeSagaFailed       = "saga_failed"       // a saga failed; the record says what is in effect
	CodeSagaUnfinished   = "saga_unfinished"   // a saga did not finish: apply it again, or take it back
	CodeNothingInstalled = "nothing_installed" // nothing is installed to remove or roll back
	CodeForeignComments  = "foreign_comments"  // values.yaml has comments pack did not write
	CodeTarget           = "target"            // the target refused, or could not be reached
	CodeNeedsApproval    = "needs_approval"    // run.approve keeps the plan from being applied unasked: --approve <its digest>
	CodeUnhealthy        = "unhealthy"         // what is installed drifted from the record, or a key is not ready (pack status)
	CodeError            = "error"             // anything else
)

// Error is an error as --json writes it.
type Error struct {
	Code     string           `json:"code"`
	Message  string           `json:"message"`
	Problems []render.Problem `json:"problems,omitempty"`
	Detail   any              `json:"detail,omitempty"` // what the command found: the scenarios of pack test, what pack author check says
}

// Failure is the document of a command that failed (--json): what went wrong, and what to run next.
type Failure struct {
	Error Error    `json:"error"`
	Next  []string `json:"next,omitempty"`
}

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

// FromEntry is an entry of a saga's journal as an event: the same as the saga runs and as pack log reads it back.
func FromEntry(e saga.Entry) Event {
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
