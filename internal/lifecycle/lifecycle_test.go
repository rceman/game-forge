package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rceman/game-forge/internal/daemon"
	"github.com/rceman/game-forge/internal/service"
)

// fakeManager records calls and drives installed/available state.
type fakeManager struct {
	avail     error
	installed bool
	enabled   bool
	started   bool
	stopped   bool
	restarted bool
}

func (f *fakeManager) Name() string           { return "fake svc" }
func (f *fakeManager) Available() error       { return f.avail }
func (f *fakeManager) Installed() bool        { return f.installed }
func (f *fakeManager) Install(string) error   { return nil }
func (f *fakeManager) Uninstall() error       { f.installed = false; return nil }
func (f *fakeManager) Start() error           { f.started = true; return nil }
func (f *fakeManager) Stop() error            { f.stopped = true; return nil }
func (f *fakeManager) Restart() error         { f.restarted = true; return nil }
func (f *fakeManager) Active() (bool, error)  { return false, nil }
func (f *fakeManager) Enabled() (bool, error) { return f.enabled, nil }

func withFake(t *testing.T, f *fakeManager) {
	t.Helper()
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	old := manager
	manager = func() service.Manager { return f }
	oldGone, oldHealthy := goneTimeout, healthyTimeout
	goneTimeout, healthyTimeout = 2*time.Second, 300*time.Millisecond
	t.Cleanup(func() {
		manager = old
		goneTimeout, healthyTimeout = oldGone, oldHealthy
	})
}

func TestInstalledRoutesToServiceManager(t *testing.T) {
	f := &fakeManager{installed: true}
	withFake(t, f)
	// No daemon will answer; health wait fails fast via the shrunken timeout —
	// the assertion is that the service manager's Start was used.
	if _, err := Start(context.Background()); err == nil {
		t.Fatal("expected health timeout with no daemon")
	}
	if !f.started {
		t.Error("installed service: expected manager Start, not detached spawn")
	}
}

func TestStopRoutesToServiceManager(t *testing.T) {
	f := &fakeManager{installed: true}
	withFake(t, f)
	// With no daemon and no lock file, WaitGone returns immediately.
	if err := Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !f.stopped {
		t.Error("installed service: expected manager Stop")
	}
}

func TestRestartRoutesToServiceManager(t *testing.T) {
	f := &fakeManager{installed: true}
	withFake(t, f)
	if _, err := Restart(context.Background()); err == nil {
		t.Fatal("expected health timeout with no daemon")
	}
	if !f.restarted {
		t.Error("installed service: expected manager Restart")
	}
}

func TestUninstalledStopIsImmediateNoop(t *testing.T) {
	f := &fakeManager{}
	withFake(t, f)
	if err := Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if f.stopped {
		t.Error("no service manager should be invoked when not installed")
	}
}

func TestWaitGoneWaitsForOwnershipRelease(t *testing.T) {
	f := &fakeManager{}
	withFake(t, f)
	// Simulate a draining predecessor: owned lock held by a live pid.
	lockPath, err := daemon.OwnedLockPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- WaitGone(context.Background()) }()
	select {
	case <-done:
		t.Fatal("WaitGone returned while a live process held ownership")
	case <-time.After(150 * time.Millisecond):
	}
	os.Remove(lockPath)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitGone: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitGone did not return after ownership release")
	}
}

func TestStatusNeverExposesCredentials(t *testing.T) {
	f := &fakeManager{}
	withFake(t, f)
	st := Stat(context.Background())
	if st.Running {
		t.Fatal("no daemon should be running in the test env")
	}
	if strings.Contains(st.Service, "token") || strings.Contains(st.Service, "bearer") {
		t.Errorf("service label leaks credentials: %q", st.Service)
	}
	if st.Service != "not installed" {
		t.Errorf("service = %q, want not installed", st.Service)
	}
	f.installed, f.enabled = true, true
	st = Stat(context.Background())
	if st.Service != "fake svc (enabled)" {
		t.Errorf("service = %q", st.Service)
	}
}

func TestUnavailableBackendFailsClearly(t *testing.T) {
	f := &fakeManager{avail: errors.New("systemd user services are not available in this environment")}
	withFake(t, f)
	if Installed() {
		t.Error("unavailable backend must not report installed")
	}
	st := Stat(context.Background())
	if st.Service != "not available" {
		t.Errorf("service = %q, want not available", st.Service)
	}
}
