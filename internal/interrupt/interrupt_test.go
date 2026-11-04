package interrupt

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

// The first interrupt is heard, then cancels the context with ErrInterrupted; stop cancels it with no cause of one.
func TestContext(t *testing.T) {
	heard := make(chan os.Signal, 1)
	Heard = func(s os.Signal) { heard <- s }
	t.Cleanup(func() { Heard = nil })
	ctx, stop := Context()
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("not cancelled")
	}
	if s := <-heard; s != os.Interrupt {
		t.Errorf("heard %v", s)
	}
	if !errors.Is(context.Cause(ctx), ErrInterrupted) {
		t.Errorf("cause: %v", context.Cause(ctx))
	}

	Heard = nil
	ctx, stop = Context()
	stop()
	if errors.Is(context.Cause(ctx), ErrInterrupted) {
		t.Error("stop is no interrupt")
	}
}
