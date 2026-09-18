//go:build windows

package daemon

import (
	"os/exec"
	"syscall"
)

// Windows process-creation flags. Go's syscall package does not export these,
// so they are defined here from the documented values.
const (
	detachedProcess       = 0x00000008 // DETACHED_PROCESS
	createNewProcessGroup = 0x00000200 // CREATE_NEW_PROCESS_GROUP
	createNoWindow        = 0x08000000 // CREATE_NO_WINDOW
)

// Detach prepares a daemon child to outlive its parent: a detached process in
// its own process group with no console window, and no inherited handles that
// would make the caller wait for its EOF.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup | createNoWindow,
	}
}
