//go:build !windows

package testkit

import (
	"os/exec"
	"syscall"
)

// ownGroup starts cmd in a process group of its own, so killGroup ends the processes
// it starts too.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killGroup(cmd *exec.Cmd) { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
