//go:build unix

package daemon

import "syscall"

// processAlive reports whether pid refers to a live process. It never sends a
// real signal.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
