// Package profile runs a project's declared validation profiles.
//
// A profile is an ordered list of stages. Game Forge owns execution, timeouts,
// browser lifecycle, fresh-error collection, timing, reporting and exit status.
// The project owns the commands and the game-specific check expressions; Game
// Forge never interprets what a check means beyond pass/fail.
package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rceman/game-forge/internal/browser"
	"github.com/rceman/game-forge/internal/project"
	"github.com/rceman/game-forge/internal/runner"
	"github.com/rceman/game-forge/internal/server"
	"github.com/rceman/game-forge/internal/visual"
)

// Deps are the collaborators a profile run needs.
type Deps struct {
	Manifest *project.Manifest
	Root     string
	LogDir   string
	// OpenBrowser opens a browser session against url and returns a cleanup.
	OpenBrowser func(ctx context.Context, url string) (*browser.Session, func(), error)
	// OpenBrowserRaw opens a session without waiting for the project bridge,
	// for production builds that do not expose it.
	OpenBrowserRaw func(ctx context.Context, url string) (*browser.Session, func(), error)
	// EnsureServer starts or reuses a declared server ("dev" or "prod").
	EnsureServer func(ctx context.Context, kind string, lease time.Duration) (*server.Server, error)
}

// StageResult is the outcome of one stage.
type StageResult struct {
	Name     string        `json:"name"`
	OK       bool          `json:"ok"`
	Duration time.Duration `json:"-"`
	Detail   string        `json:"detail,omitempty"`
	Output   string        `json:"output,omitempty"`
}

// Summary is the outcome of a profile run.
type Summary struct {
	Profile  string        `json:"profile"`
	Stages   []StageResult `json:"stages"`
	OK       bool          `json:"ok"`
	Duration time.Duration `json:"-"`
}

// Run executes a named profile.
func Run(ctx context.Context, d Deps, name string) (*Summary, error) {
	stages, ok := d.Manifest.Profile(name)
	if !ok {
		return nil, fmt.Errorf("unknown profile %q", name)
	}
	sum := &Summary{Profile: name, OK: true}
	start := time.Now()
	if err := d.runStages(ctx, sum, stages, 0); err != nil {
		return sum, err
	}
	sum.Duration = time.Since(start)
	for _, s := range sum.Stages {
		if !s.OK {
			sum.OK = false
		}
	}
	return sum, nil
}

func (d Deps) runStages(ctx context.Context, sum *Summary, stages []project.Stage, depth int) error {
	if depth > 8 {
		return fmt.Errorf("profile reference depth exceeded (cycle?)")
	}
	for _, stage := range stages {
		if stage.Uses != "" {
			sub, err := d.runNested(ctx, stage, depth)
			if err != nil {
				return err
			}
			sum.Stages = append(sum.Stages, sub...)
			continue
		}
		res, err := d.runStage(ctx, stage, depth)
		if err != nil {
			res = StageResult{Name: stage.Name, OK: false, Detail: err.Error()}
		}
		sum.Stages = append(sum.Stages, res)
	}
	return nil
}

func (d Deps) runNested(ctx context.Context, stage project.Stage, depth int) ([]StageResult, error) {
	stages, ok := d.Manifest.Profile(stage.Uses)
	if !ok {
		return nil, fmt.Errorf("stage %q: unknown profile %q", stage.Name, stage.Uses)
	}
	var out []StageResult
	for _, s := range stages {
		if s.Uses != "" {
			sub, err := d.runNested(ctx, s, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
			continue
		}
		res, err := d.runStage(ctx, s, depth+1)
		if err != nil {
			res = StageResult{Name: s.Name, OK: false, Detail: err.Error()}
		}
		res.Name = stage.Name + "/" + res.Name
		out = append(out, res)
	}
	return out, nil
}

func (d Deps) runStage(ctx context.Context, stage project.Stage, depth int) (StageResult, error) {
	timeout := stage.Timeout.Std()
	switch {
	case len(stage.Command) > 0:
		return d.runCommand(ctx, stage, timeout)
	case stage.Sweep:
		return d.runSweep(ctx, stage, timeout)
	case stage.Prod:
		return d.runProd(ctx, stage, timeout)
	case stage.Browser:
		return d.runBrowser(ctx, stage, timeout)
	default:
		return StageResult{}, fmt.Errorf("stage %q has no runnable mode", stage.Name)
	}
}

func (d Deps) runCommand(ctx context.Context, stage project.Stage, timeout time.Duration) (StageResult, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	res, err := runner.Run(ctx, d.Root, stage.Command, timeout)
	if err != nil {
		return StageResult{Name: stage.Name}, err
	}
	out := StageResult{
		Name:     stage.Name,
		OK:       res.OK(),
		Duration: res.Duration,
		Detail:   fmt.Sprintf("exit=%d", res.ExitCode),
		Output:   res.Output,
	}
	return out, nil
}

func (d Deps) runBrowser(ctx context.Context, stage project.Stage, timeout time.Duration) (StageResult, error) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	srv, err := d.EnsureServer(ctx, "dev", 0)
	if err != nil {
		return StageResult{Name: stage.Name}, err
	}
	sess, cleanup, err := d.OpenBrowser(ctx, srv.URL())
	if err != nil {
		return StageResult{Name: stage.Name}, err
	}
	defer cleanup()

	start := time.Now()
	_ = sess.P.ClearErrors(ctx)
	if stage.Click != "" {
		if err := sess.P.Click(ctx, stage.Click); err != nil {
			return StageResult{Name: stage.Name, Duration: time.Since(start)}, fmt.Errorf("click %q: %w", stage.Click, err)
		}
	}
	ok, detail, err := evalCheck(ctx, sess, stage.Eval)
	if err != nil {
		return StageResult{Name: stage.Name, Duration: time.Since(start)}, err
	}
	if ok {
		if errs := visual.FilterNoise(mustErrors(ctx, sess)); len(errs) > 0 {
			return StageResult{Name: stage.Name, OK: false, Duration: time.Since(start), Detail: "fresh page error: " + strings.Join(errs, "; ")}, nil
		}
		if stage.Console {
			if errs := consoleErrors(ctx, sess); len(errs) > 0 {
				return StageResult{Name: stage.Name, OK: false, Duration: time.Since(start), Detail: "console errors: " + strings.Join(errs, "; ")}, nil
			}
		}
	}
	return StageResult{Name: stage.Name, OK: ok, Duration: time.Since(start), Detail: detail}, nil
}

func (d Deps) runSweep(ctx context.Context, stage project.Stage, timeout time.Duration) (StageResult, error) {
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	srv, err := d.EnsureServer(ctx, "dev", 0)
	if err != nil {
		return StageResult{Name: stage.Name}, err
	}
	sess, cleanup, err := d.OpenBrowser(ctx, srv.URL())
	if err != nil {
		return StageResult{Name: stage.Name}, err
	}
	defer cleanup()

	start := time.Now()
	results, err := visual.Sweep(ctx, sess, stage.Ticks)
	if err != nil {
		return StageResult{Name: stage.Name, Duration: time.Since(start)}, err
	}
	failed := 0
	var lines []string
	for _, r := range results {
		mark := "ok"
		if !r.OK {
			mark = "FAIL"
			failed++
		}
		line := fmt.Sprintf("%-24s %s", r.Case, mark)
		if r.Err != "" {
			line += ": " + r.Err
		}
		lines = append(lines, line)
	}
	return StageResult{
		Name:     stage.Name,
		OK:       failed == 0,
		Duration: time.Since(start),
		Detail:   fmt.Sprintf("%d cases, %d failed", len(results), failed),
		Output:   strings.Join(lines, "\n"),
	}, nil
}

func (d Deps) runProd(ctx context.Context, stage project.Stage, timeout time.Duration) (StageResult, error) {
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()

	prod := d.Manifest.Server.Prod
	if len(prod.Build) > 0 {
		buildRes, err := runner.Run(ctx, d.Root, prod.Build, 5*time.Minute)
		if err != nil {
			return StageResult{Name: stage.Name}, err
		}
		if !buildRes.OK() {
			return StageResult{Name: stage.Name, OK: false, Duration: time.Since(start), Detail: "production build failed", Output: buildRes.Output}, nil
		}
	}
	srv, err := d.EnsureServer(ctx, "prod", 0)
	if err != nil {
		return StageResult{Name: stage.Name}, err
	}
	defer srv.Stop()

	open := d.OpenBrowserRaw
	if open == nil {
		open = d.OpenBrowser
	}
	sess, cleanup, err := open(ctx, srv.URL())
	if err != nil {
		return StageResult{Name: stage.Name}, err
	}
	defer cleanup()

	_ = sess.P.ClearErrors(ctx)
	if stage.Click != "" {
		if err := sess.P.Click(ctx, stage.Click); err != nil {
			return StageResult{Name: stage.Name, Duration: time.Since(start)}, fmt.Errorf("click %q: %w", stage.Click, err)
		}
	}
	ok, detail, err := evalCheck(ctx, sess, stage.Eval)
	if err != nil {
		return StageResult{Name: stage.Name, Duration: time.Since(start)}, err
	}
	if ok {
		if errs := visual.FilterNoise(mustErrors(ctx, sess)); len(errs) > 0 {
			return StageResult{Name: stage.Name, OK: false, Duration: time.Since(start), Detail: "fresh page error: " + strings.Join(errs, "; ")}, nil
		}
	}
	return StageResult{Name: stage.Name, OK: ok, Duration: time.Since(start), Detail: detail}, nil
}

// evalCheck evaluates a project check expression and interprets pass/fail.
func evalCheck(ctx context.Context, sess *browser.Session, expr string) (bool, string, error) {
	raw, err := sess.P.Eval(ctx, expr)
	if err != nil {
		return false, "", err
	}
	var val any
	if err := json.Unmarshal(raw, &val); err != nil {
		return false, string(raw), nil
	}
	switch v := val.(type) {
	case bool:
		return v, fmt.Sprintf("%v", v), nil
	case string:
		up := strings.ToUpper(strings.TrimSpace(v))
		switch {
		case strings.HasPrefix(up, "PASS"):
			return true, firstLine(v), nil
		case strings.HasPrefix(up, "FAIL"):
			return false, firstLine(v), nil
		}
		return v != "", firstLine(v), nil
	case nil:
		return false, "check returned null", nil
	default:
		b, _ := json.Marshal(v)
		return true, string(b), nil
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func mustErrors(ctx context.Context, sess *browser.Session) []string {
	errs, err := sess.P.PageErrors(ctx)
	if err != nil {
		return nil
	}
	return errs
}

func consoleErrors(ctx context.Context, sess *browser.Session) []string {
	msgs, err := sess.P.ConsoleErrors(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range visual.FilterNoise(msgs) {
		low := strings.ToLower(m)
		if strings.Contains(low, "error") || strings.Contains(low, "warn") {
			out = append(out, m)
		}
	}
	return out
}

// Format renders a compact profile summary.
func (s *Summary) Format(verbose bool) string {
	var b strings.Builder
	for _, st := range s.Stages {
		mark := "PASS"
		if !st.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "%-6s %-28s %s\n", mark, st.Name, st.Detail)
		if verbose && st.Output != "" {
			for _, line := range strings.Split(strings.TrimRight(st.Output, "\n"), "\n") {
				fmt.Fprintf(&b, "       | %s\n", line)
			}
		}
	}
	verdict := "PASS"
	if !s.OK {
		verdict = "FAIL"
	}
	fmt.Fprintf(&b, "%s profile=%s stages=%d duration=%s", verdict, s.Profile, len(s.Stages), s.Duration.Round(time.Millisecond))
	return b.String()
}
