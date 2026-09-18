// Package runner executes native project tools on behalf of Game Forge.
//
// Game Forge orchestrates existing tools (typecheck, test, build) rather than
// replacing them. It preserves the true exit code, bounds the runtime, and
// reports timing — but it never pipes output through a filter that could turn a
// failure into a success.
package runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// Result is the normalized outcome of a native command.
type Result struct {
	Name     string
	Command  []string
	ExitCode int
	Duration time.Duration
	Output   string
}

// OK reports whether the command succeeded.
func (r *Result) OK() bool { return r.ExitCode == 0 }

// Run executes command in dir, capturing combined output.
//
// A non-zero exit is reported in Result.ExitCode, not as a Go error. A Go error
// is returned only when the command could not be run at all.
func Run(ctx context.Context, dir string, command []string, timeout time.Duration) (*Result, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("command is empty")
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	start := time.Now()
	err := cmd.Run()
	res := &Result{
		Command:  command,
		Duration: time.Since(start),
		Output:   buf.String(),
	}
	if err == nil {
		return res, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		res.ExitCode = 124
		res.Output += fmt.Sprintf("\n[game-forge] timed out after %s", timeout)
		return res, nil
	}
	return nil, fmt.Errorf("run %v: %w", command, err)
}
