//go:build !windows

package git

import (
	"os/exec"
	"syscall"
)

// setProcGroup starts git in its own process group so a timeout or
// cancellation can kill git and every helper it spawned (hooks are disabled,
// but aliases, merge drivers and pack helpers are still child processes).
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcGroup sends SIGKILL to the whole process group (negative pid).
func killProcGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
