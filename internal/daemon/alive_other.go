//go:build !unix

package daemon

// processAlive is conservative on platforms where cheap liveness probing is
// not implemented: it defers to the health check, which is authoritative.
func processAlive(pid int) bool { return pid > 0 }
