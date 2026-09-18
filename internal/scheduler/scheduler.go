// Package scheduler installs a per-user OS schedule that periodically runs
// "game-forge tick".
//
// Game Forge does not run a permanent cleanup daemon. It uses the operating
// system's own scheduler, at a one-minute cadence, for stale-resource cleanup.
//
// On this machine (WSL with cron running, but no per-user systemd bus) the
// supported backend is the per-user crontab. It needs no root and no
// privileged service.
package scheduler

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// Marker identifies the lines Game Forge owns in the user's crontab.
const Marker = "# game-forge-managed-tick"

// Backend is an OS scheduler integration.
type Backend interface {
	// Name identifies the backend, e.g. "crontab".
	Name() string
	// Install schedules command to run every minute.
	Install(command, logPath string) error
	// Status reports whether the schedule is installed and its entry.
	Status() (bool, string, error)
	// Uninstall removes the schedule.
	Uninstall() error
}

// Detect returns the scheduler backend for the current environment.
func Detect() (Backend, error) {
	if _, err := exec.LookPath("crontab"); err == nil {
		return &cronBackend{}, nil
	}
	return nil, fmt.Errorf("no supported scheduler backend found (crontab is unavailable)")
}

type cronBackend struct{}

func (c *cronBackend) Name() string { return "crontab" }

// read returns the current crontab lines (empty when none is installed).
func (c *cronBackend) read() ([]string, error) {
	cmd := exec.Command("crontab", "-l")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		if strings.Contains(errb.String(), "no crontab") {
			return nil, nil
		}
		if _, ok := err.(*exec.ExitError); ok && out.Len() == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("read crontab: %w", err)
	}
	text := strings.TrimRight(out.String(), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

// write replaces the crontab with lines.
func (c *cronBackend) write(lines []string) error {
	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write crontab: %w (%s)", err, strings.TrimSpace(errb.String()))
	}
	return nil
}

// without removes Game Forge-owned lines.
func without(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.Contains(l, Marker) {
			continue
		}
		out = append(out, l)
	}
	return out
}

func (c *cronBackend) Install(command, logPath string) error {
	lines, err := c.read()
	if err != nil {
		return err
	}
	lines = without(lines)
	entry := fmt.Sprintf("* * * * * %s tick >> %s 2>&1 %s", command, logPath, Marker)
	lines = append(lines, entry)
	return c.write(lines)
}

func (c *cronBackend) Status() (bool, string, error) {
	lines, err := c.read()
	if err != nil {
		return false, "", err
	}
	for _, l := range lines {
		if strings.Contains(l, Marker) {
			return true, l, nil
		}
	}
	return false, "", nil
}

func (c *cronBackend) Uninstall() error {
	lines, err := c.read()
	if err != nil {
		return err
	}
	return c.write(without(lines))
}
