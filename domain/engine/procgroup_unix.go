//go:build !windows

package engine

import (
	"os/exec"
	"syscall"
)

// setProcGroup configures the command to run in its own process group so that
// all child processes can be killed together on timeout.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcGroup sends SIGKILL to the entire process group.
func killProcGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		// Kill the entire process group (negative PID).
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
