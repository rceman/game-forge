//go:build linux

package service

// Current returns the platform's service manager: systemd --user on Linux.
func Current() Manager { return newSystemd(defaultUnitDir(), runSystemctl) }
