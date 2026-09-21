// Package lifecycle is the single implementation behind both the human-facing
// lifecycle commands (start/stop/restart/status) and the administrative
// `daemon` aliases. It routes to the native service manager when a Game Forge
// service is installed, and to detached-process management otherwise.
package lifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/rceman/game-forge/internal/client"
	"github.com/rceman/game-forge/internal/daemon"
	"github.com/rceman/game-forge/internal/service"
)

// goneTimeout bounds the wait for a daemon incarnation to fully relinquish
// ownership (endpoint closed + resource drain + lifetime lock released).
var goneTimeout = 45 * time.Second

// healthyTimeout bounds the wait for a service-managed start to report healthy.
var healthyTimeout = 45 * time.Second

// manager is the service backend; it is a var so tests can substitute a fake.
var manager = service.Current

// Status is the rendered lifecycle state.
type Status struct {
	Running  bool
	PID      int
	Endpoint string
	// Service describes the management backend, e.g. "systemd user
	// (enabled)", "manual", or "not installed".
	Service string
}

// Installed reports whether a native service registration exists.
func Installed() bool {
	m := manager()
	return m.Available() == nil && m.Installed()
}

// serviceLabel describes the service-management state for status output.
func serviceLabel() string {
	m := manager()
	if err := m.Available(); err != nil {
		return "not available"
	}
	if !m.Installed() {
		return "not installed"
	}
	enabled, _ := m.Enabled()
	if enabled {
		return m.Name() + " (enabled)"
	}
	return m.Name() + " (installed)"
}

// Stat returns the current lifecycle state.
func Stat(ctx context.Context) *Status {
	st := &Status{Service: serviceLabel()}
	if cl := client.Connect(ctx); cl != nil {
		d := cl.Discovery()
		st.Running = true
		st.PID = d.PID
		st.Endpoint = d.Endpoint
		if st.Service == "not installed" {
			st.Service = "manual"
		}
	}
	return st
}

// Start ensures Game Forge is running — via the service manager when
// installed, otherwise by spawning a detached daemon.
func Start(ctx context.Context) (*client.Client, error) {
	if Installed() {
		if cl := client.Connect(ctx); cl != nil {
			return cl, nil
		}
		if err := manager().Start(); err != nil {
			return nil, err
		}
		return waitHealthy(ctx)
	}
	return client.Ensure(ctx)
}

// Ensure is the transparent-start path used by ordinary commands: same
// routing as Start, so an installed service is never bypassed by a detached
// spawn.
func Ensure(ctx context.Context) (*client.Client, error) {
	return Start(ctx)
}

// Stop stops Game Forge and returns only once the previous incarnation has
// fully relinquished ownership — endpoint closed, resources drained, lifetime
// lock released — so an immediate start always works.
func Stop(ctx context.Context) error {
	if Installed() {
		if err := manager().Stop(); err != nil {
			return err
		}
		return WaitGone(ctx)
	}
	cl := client.Connect(ctx)
	if cl == nil {
		return nil
	}
	if err := cl.Shutdown(ctx); err != nil {
		return err
	}
	return WaitGone(ctx)
}

// Restart replaces the running incarnation cleanly.
func Restart(ctx context.Context) (*client.Client, error) {
	if Installed() {
		if err := manager().Restart(); err != nil {
			return nil, err
		}
		return waitHealthy(ctx)
	}
	if err := Stop(ctx); err != nil {
		return nil, err
	}
	return Start(ctx)
}

// waitHealthy polls until the daemon answers an authenticated health check.
func waitHealthy(ctx context.Context) (*client.Client, error) {
	deadline := time.Now().Add(healthyTimeout)
	for time.Now().Before(deadline) {
		if cl := client.Connect(ctx); cl != nil {
			return cl, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("game-forged did not become healthy within %s", healthyTimeout)
}

// WaitGone waits until the previous daemon has fully released ownership —
// endpoint closed, resource drain done, lifetime lock released. Install and
// rebind use it to transition cleanly between detached and service ownership.
func WaitGone(ctx context.Context) error {
	deadline := time.Now().Add(goneTimeout)
	for time.Now().Before(deadline) {
		if client.Connect(ctx) == nil && daemon.OwnershipFree() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("game-forged did not release ownership within %s", goneTimeout)
}
