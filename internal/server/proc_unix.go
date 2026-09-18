//go:build unix

package server

import (
	"os/exec"
	"syscall"
)

// setProcessGroup places the command in its own process group so the whole
// tree can be terminated together.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup terminates the command's process group, then the process.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil && pgid > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	}
	_ = cmd.Process.Kill()
}

// processGroupID returns the command's process group id.
func processGroupID(cmd *exec.Cmd) int {
	if cmd.Process == nil {
		return 0
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return 0
	}
	return pgid
}

// KillProcessGroup terminates an owned process group (falling back to the pid).
func KillProcessGroup(pid, pgid int) {
	if pgid > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	}
	if pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
}
