// Package interrupt turns the first interrupt into a cancelled context.
package interrupt

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
)

// ErrInterrupted is the cause of a context an interrupt cancelled.
var ErrInterrupted = errors.New("interrupted")

// Heard, when set before Context, hears the first interrupt as it comes, before the context is cancelled: what
// --stream says of it.
var Heard func(os.Signal)

// Context is the context of a command: the first interrupt (Ctrl-C, SIGTERM) cancels it, the saga cuts the
// running steps short and rolls back; after it the signals are the system's again, so a second one ends pack.
func Context() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	heard := Heard
	go func() {
		defer signal.Stop(ch)
		select {
		case s := <-ch:
			if heard != nil {
				heard(s)
			}
			cancel(ErrInterrupted)
		case <-ctx.Done():
		}
	}()
	return ctx, func() { cancel(nil) }
}
