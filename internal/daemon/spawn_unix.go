//go:build unix

package daemon

import (
	"os/exec"
	"syscall"
)

// detach prepares a daemon child to outlive its parent: its own session, and
// no inherited handles that would make the caller wait for its EOF.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
