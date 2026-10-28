package testkit

import "os/exec"

// The scenarios that kill the hub run on Linux and macOS; on Windows Kill ends the
// hub alone.
func ownGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) { cmd.Process.Kill() }
