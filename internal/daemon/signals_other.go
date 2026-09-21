//go:build !unix

package daemon

import "os"

// stopSignals is conservative on platforms without POSIX signals.
func stopSignals() []os.Signal { return []os.Signal{os.Interrupt} }
