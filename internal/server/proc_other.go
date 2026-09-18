//go:build !unix

package server

import (
	"os"
	"os/exec"
)

// setProcessGroup is a no-op on platforms without process groups.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup terminates the process.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// processGroupID is unavailable without process groups.
func processGroupID(cmd *exec.Cmd) int { return 0 }

// KillProcessGroup terminates a single process.
func KillProcessGroup(pid, pgid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
