//go:build !unix

package remote

import "os/exec"

func detach(*exec.Cmd) {}
