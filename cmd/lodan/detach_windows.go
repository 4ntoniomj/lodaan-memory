//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup  = 0x00000200
	createNoWindow         = 0x08000000
	createBreakawayFromJob = 0x01000000
)

// startDetached starts cmd without a console window and, when the caller's job allows it,
// outside that job, so the AI client does not kill it when the hook ends.
func startDetached(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | createNoWindow | createBreakawayFromJob}
	if err := cmd.Start(); err == nil {
		return nil
	}
	// The job does not allow breaking away: start it inside the job.
	retry := exec.Command(cmd.Path, cmd.Args[1:]...)
	retry.Env, retry.Dir = cmd.Env, cmd.Dir
	retry.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | createNoWindow}
	*cmd = *retry
	return cmd.Start()
}
