//go:build unix

package daemon

import (
	"os"
	"syscall"
)

// stopSignals are the termination signals that trigger graceful shutdown.
// SIGTERM is how service managers (systemd --user) stop and restart the
// daemon; SIGINT covers interactive Ctrl-C on a foreground serve.
func stopSignals() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM} }
