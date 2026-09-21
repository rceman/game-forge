//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// systemd is the Linux backend: a per-user unit under
// ~/.config/systemd/user managed through `systemctl --user`.
type systemd struct {
	unitDir string
	run     func(args ...string) (string, error)
}

// runSystemctl executes `systemctl --user` and returns combined output.
var runSystemctl = func(args ...string) (string, error) {
	full := append([]string{"--user"}, args...)
	out, err := exec.Command("systemctl", full...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// defaultUnitDir resolves the per-user systemd unit directory, honouring
// XDG_CONFIG_HOME.
func defaultUnitDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "systemd", "user")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user")
}

func newSystemd(unitDir string, run func(args ...string) (string, error)) *systemd {
	return &systemd{unitDir: unitDir, run: run}
}

func (s *systemd) Name() string { return "systemd user" }

func (s *systemd) unitPath() string { return filepath.Join(s.unitDir, Unit) }

// Available verifies a usable systemd user manager: the binary exists and
// `systemctl --user` can reach the user bus.
func (s *systemd) Available() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemd user services are not available (systemctl not found)")
	}
	out, err := s.run("is-system-running")
	// is-system-running exits non-zero for degraded/offline; accept the
	// states where the user manager is usable anyway.
	if err == nil {
		return nil
	}
	switch out {
	case "running", "degraded", "starting", "maintenance", "initializing":
		return nil
	}
	if out != "" {
		return fmt.Errorf("systemd user services are not available in this environment (%s)", out)
	}
	return fmt.Errorf("systemd user services are not available in this environment")
}

// unitFile renders the user unit. The worker stays attached to systemd as a
// plain foreground process; systemd owns its lifecycle.
func unitFile(binPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Game Forge development daemon (game-forged)
# A dev daemon is restarted routinely; the default 5-per-10s start limit would
# turn rapid restart loops into permanent unit failures.
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart=%s daemon serve
Restart=on-failure
RestartSec=2s

[Install]
WantedBy=default.target
`, binPath)
}

func (s *systemd) Installed() bool {
	_, err := os.Stat(s.unitPath())
	return err == nil
}

// Install atomically writes or refreshes the unit (a moved/rebuilt binary
// updates ExecStart in place), reloads systemd and enables the unit. It does
// not start the service.
func (s *systemd) Install(binPath string) error {
	if err := s.Available(); err != nil {
		return err
	}
	abs, err := filepath.Abs(binPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.unitDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.unitDir, "game-forged-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(unitFile(abs)); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, s.unitPath()); err != nil {
		os.Remove(tmpName)
		return err
	}
	if out, err := s.run("daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %s", out)
	}
	if out, err := s.run("enable", Unit); err != nil {
		return fmt.Errorf("systemctl enable: %s", out)
	}
	return nil
}

// Uninstall stops, disables and removes the unit, then reloads systemd.
// Durable Game Forge state (~/.game-forge) is never touched.
func (s *systemd) Uninstall() error {
	if err := s.Available(); err != nil {
		return err
	}
	_, _ = s.run("stop", Unit)
	_, _ = s.run("disable", Unit)
	err := os.Remove(s.unitPath())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if out, err := s.run("daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %s", out)
	}
	return nil
}

func (s *systemd) Start() error {
	out, err := s.run("start", Unit)
	if err != nil {
		return fmt.Errorf("systemctl start: %s", out)
	}
	return nil
}

func (s *systemd) Stop() error {
	out, err := s.run("stop", Unit)
	if err != nil {
		return fmt.Errorf("systemctl stop: %s", out)
	}
	return nil
}

func (s *systemd) Restart() error {
	out, err := s.run("restart", Unit)
	if err != nil {
		return fmt.Errorf("systemctl restart: %s", out)
	}
	return nil
}

func (s *systemd) Active() (bool, error) {
	out, err := s.run("is-active", Unit)
	return out == "active" && err == nil, nil
}

func (s *systemd) Enabled() (bool, error) {
	out, err := s.run("is-enabled", Unit)
	if err != nil {
		return false, err
	}
	return out == "enabled", nil
}
