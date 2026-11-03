//go:build unix

package remote

import (
	"errors"
	"syscall"
)

// alive says whether a process of this machine runs: a signal 0 reaches it, or is refused only for want of the right.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
