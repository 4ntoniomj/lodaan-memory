//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// startDetached starts cmd in its own session, so it outlives the hook that started it.
func startDetached(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}
