//go:build !linux

package service

// Current returns the unsupported manager on platforms without a backend yet.
func Current() Manager { return unsupported{} }
