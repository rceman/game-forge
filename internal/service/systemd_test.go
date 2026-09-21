//go:build linux

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner records systemctl invocations and returns canned output.
type fakeRunner struct {
	calls   [][]string
	outputs map[string]string // first arg -> stdout
	errs    map[string]error
}

func (f *fakeRunner) run(args ...string) (string, error) {
	f.calls = append(f.calls, args)
	key := args[0]
	if err := f.errs[key]; err != nil {
		return f.outputs[key], err
	}
	return f.outputs[key], nil
}

func testSystemd(t *testing.T) (*systemd, *fakeRunner) {
	t.Helper()
	f := &fakeRunner{outputs: map[string]string{"is-system-running": "running"}, errs: map[string]error{}}
	return newSystemd(t.TempDir(), f.run), f
}

func TestUnitFileContent(t *testing.T) {
	u := unitFile("/home/u/bin/game-forge")
	for _, want := range []string{
		"ExecStart=/home/u/bin/game-forge daemon serve",
		"Restart=on-failure",
		"WantedBy=default.target",
		"Type=simple",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
	if strings.Contains(u, "fork") {
		t.Error("unit must not fork — systemd owns the worker process")
	}
}

func TestInstallWritesUnitEnables(t *testing.T) {
	s, f := testSystemd(t)
	if err := s.Install("/home/u/bin/game-forge"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	data, err := os.ReadFile(s.unitPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ExecStart=/home/u/bin/game-forge daemon serve") {
		t.Errorf("unit content wrong:\n%s", data)
	}
	var reload, enable bool
	for _, c := range f.calls {
		if c[0] == "daemon-reload" {
			reload = true
		}
		if c[0] == "enable" && c[1] == Unit {
			enable = true
		}
	}
	if !reload || !enable {
		t.Errorf("expected daemon-reload+enable, calls: %v", f.calls)
	}
}

func TestInstallRefreshesExecStart(t *testing.T) {
	s, _ := testSystemd(t)
	if err := s.Install("/old/path/game-forge"); err != nil {
		t.Fatal(err)
	}
	// Reinstall from a new binary path: the same unit is updated, not duplicated.
	if err := s.Install("/new/path/game-forge"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.unitPath())
	if !strings.Contains(string(data), "ExecStart=/new/path/game-forge daemon serve") {
		t.Errorf("reinstall did not update ExecStart:\n%s", data)
	}
	entries, _ := os.ReadDir(s.unitDir)
	var units int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".service") {
			units++
		}
	}
	if units != 1 {
		t.Errorf("expected exactly one unit file, found %d", units)
	}
}

func TestUninstallPreservesGameForgeState(t *testing.T) {
	s, _ := testSystemd(t)
	if err := s.Install("/b/game-forge"); err != nil {
		t.Fatal(err)
	}
	// Durable state must survive: simulate ~/.game-forge alongside.
	state := filepath.Join(filepath.Dir(s.unitDir), "game-forge-state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(s.unitPath()); !os.IsNotExist(err) {
		t.Error("unit file still present after uninstall")
	}
	if _, err := os.Stat(state); err != nil {
		t.Error("unrelated durable state was removed")
	}
	if s.Installed() {
		t.Error("Installed() = true after uninstall")
	}
}

func TestAvailableFailsWhenNoUserBus(t *testing.T) {
	f := &fakeRunner{
		outputs: map[string]string{"is-system-running": "Failed to connect to bus: No such file or directory"},
		errs:    map[string]error{"is-system-running": fmt.Errorf("exit status 1")},
	}
	s := newSystemd(t.TempDir(), f.run)
	err := s.Available()
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Errorf("expected clear unavailability, got %v", err)
	}
	if err := s.Install("/b/game-forge"); err == nil {
		t.Error("Install should fail when the backend is unavailable")
	}
}

func TestAvailableAcceptsDegraded(t *testing.T) {
	f := &fakeRunner{
		outputs: map[string]string{"is-system-running": "degraded"},
		errs:    map[string]error{"is-system-running": fmt.Errorf("exit status 1")},
	}
	s := newSystemd(t.TempDir(), f.run)
	if err := s.Available(); err != nil {
		t.Errorf("degraded user manager should still be usable: %v", err)
	}
}

func TestActiveEnabledParsing(t *testing.T) {
	s, f := testSystemd(t)
	f.outputs["is-active"] = "active"
	f.outputs["is-enabled"] = "enabled"
	a, _ := s.Active()
	e, _ := s.Enabled()
	if !a || !e {
		t.Errorf("active=%v enabled=%v", a, e)
	}
	f.outputs["is-active"] = "inactive"
	f.outputs["is-enabled"] = "disabled"
	f.errs["is-enabled"] = fmt.Errorf("exit status 1")
	a, _ = s.Active()
	e, _ = s.Enabled()
	if a || e {
		t.Errorf("active=%v enabled=%v after state change", a, e)
	}
}
