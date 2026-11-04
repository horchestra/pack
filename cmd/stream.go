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
	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/plan"
)

// --stream: what a command does, as it does it, is NDJSON on stdout — one event a line — and nothing else is there:
// the text a person reads goes to stderr. What a UI's backend forwards as it comes (an SSE `data:`, a WebSocket
// frame) without reading text. The last event is the result.
var streamOut bool

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
		streamEvent(api.Event{Type: api.EventSaga, Status: api.StatusInterrupted, Saga: id, Message: "interrupted: the running steps are cut short, the saga rolls back"})
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
func streamEvent(e api.Event) {
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
func planned(p *plan.Plan) { streamEvent(api.Event{Type: api.EventPlan, Plan: p}) }
