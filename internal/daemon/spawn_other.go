//go:build !unix && !windows

package daemon

import "os/exec"

// detach is a no-op on platforms without a dedicated detachment mechanism.
func Detach(cmd *exec.Cmd) {}
