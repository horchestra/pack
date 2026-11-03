//go:build unix

package remote

import (
	"errors"
	"os"
	"syscall"
)

// alive says whether a process of this machine runs: a signal 0 reaches it, or is refused only for want of the right.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// socketGroup is the group of a unix socket of the machine: what a container's user is given to reach it.
func socketGroup(path string) (int, bool) {
	fi, err := os.Stat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		return 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Gid), true
}
