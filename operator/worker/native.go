package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Native is the runtime of the worker's own process (К6): pack runs as a child, a cancel is SIGINT.
type Native struct {
	// Log takes the child's stderr lines (the job's log).
	Log func(line string)
}

var _ Runtime = Native{}

// Run executes the job's argv in the checkout, the events read off stdout (NDJSON of --stream).
func (n Native) Run(ctx context.Context, run *Run) (int, error) {
	argv := run.Job.Argv
	if len(argv) == 0 {
		return 1, nil
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = run.Checkout
	cmd.Env = run.Env
	// a cancel is SIGINT, not SIGKILL (К6): the saga interrupts and rolls back
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return cmd.Process.Signal(os.Interrupt)
		}
		return nil
	}
	cmd.WaitDelay = waitAfterInterrupt
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 1, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 1, err
	}
	if err := cmd.Start(); err != nil {
		return 1, err
	}
	go n.logLines(stderr, run.Job.ID)
	events := bufio.NewScanner(stdout)
	events.Buffer(make([]byte, 1024*1024), 1024*1024)
	for events.Scan() {
		line := strings.TrimSpace(events.Text())
		if len(line) > 0 && line[0] == '{' {
			run.Event(json.RawMessage(line))
		}
	}
	err = cmd.Wait()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
		err = nil
	}
	if ctx.Err() != nil && err != nil && strings.Contains(err.Error(), "signal") {
		err = nil // the interrupt's own death is the cancel, not a failure of Run
	}
	return code, err
}

// Reattach of a native job: the process died with the worker — the job is lost (К6).
func (n Native) Reattach(ctx context.Context, run *Run, lastSeq int64) (int, error) {
	return 0, ErrLost
}

// logLines reads the child's stderr into the worker's log with the job.
func (n Native) logLines(rd io.Reader, jobID string) {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 256*1024), 256*1024)
	for sc.Scan() {
		if n.Log != nil {
			n.Log(sc.Text())
		}
	}
}

// waitAfterInterrupt is how long a child has to roll back after SIGINT before the kill.
const waitAfterInterrupt = 11 * time.Minute // pack's rollback cap is 10 (A8)
