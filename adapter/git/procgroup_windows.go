//go:build windows

package git

import "os/exec"

// setProcGroup is a no-op on Windows.
func setProcGroup(_ *exec.Cmd) {}

// killProcGroup kills the git process directly on Windows.
func killProcGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
