// Package interrupt turns the first interrupt into a cancelled context.
package interrupt

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Context is the context of a command: the first interrupt (Ctrl-C, SIGTERM) cancels it, the saga cuts the
// running steps short and rolls back; after it the signals are the system's again, so a second one ends pack.
func Context() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}
