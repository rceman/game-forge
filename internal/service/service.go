// Package service abstracts the native per-user service manager that can own
// the Game Forge daemon (systemd --user today; launchd or a Windows user
// startup mechanism could be added behind the same interface).
package service

import "fmt"

// Unit is the service-manager unit name for the Game Forge daemon.
const Unit = "game-forged.service"

// Manager owns installed service lifecycle for one platform backend.
type Manager interface {
	// Name describes the backend for status output, e.g. "systemd user".
	Name() string
	// Available reports whether this backend is usable on this machine; the
	// error explains why when it is not.
	Available() error
	// Installed reports whether a Game Forge unit is registered.
	Installed() bool
	// Install writes/refreshs the unit for binPath and enables it. It does
	// not start the service — lifecycle.Start does, so install and
	// start-failure are reported separately.
	Install(binPath string) error
	// Uninstall stops and disables the service and removes the unit. Durable
	// Game Forge state is untouched.
	Uninstall() error
	Start() error
	Stop() error
	Restart() error
	// Active reports whether the service is running per the manager.
	Active() (bool, error)
	// Enabled reports whether the service starts with the user environment.
	Enabled() (bool, error)
}

// ErrUnsupported is returned when no backend exists for this platform.
var ErrUnsupported = fmt.Errorf("service management is not supported on this platform")

// unsupported is the fallback manager for platforms with no backend.
type unsupported struct{}

func (unsupported) Name() string           { return "none" }
func (unsupported) Available() error       { return ErrUnsupported }
func (unsupported) Installed() bool        { return false }
func (unsupported) Install(string) error   { return ErrUnsupported }
func (unsupported) Uninstall() error       { return ErrUnsupported }
func (unsupported) Start() error           { return ErrUnsupported }
func (unsupported) Stop() error            { return ErrUnsupported }
func (unsupported) Restart() error         { return ErrUnsupported }
func (unsupported) Active() (bool, error)  { return false, ErrUnsupported }
func (unsupported) Enabled() (bool, error) { return false, ErrUnsupported }
