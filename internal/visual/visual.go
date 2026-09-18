// Package visual orchestrates Game Forge's screenshot and sweep workflows.
//
// Game Forge owns the browser lifecycle, project navigation, scenario/visual
// loading, exact advancement, screenshot capture, fresh-error checking and
// cleanup. The game owns the visual case semantics and the named regions.
package visual

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rceman/game-forge/internal/browser"
)

// Case is one declared visual case.
type Case struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// ShotOptions configure a deterministic screenshot.
type ShotOptions struct {
	Case      string
	Ticks     int
	Seed      string
	WorldSeed string
	Output    string
	// Region is an optional game-declared named region ("full" = whole viewport).
	Region string
	// Expr is optional project JavaScript run after load/advance, before capture.
	Expr string
}

// ShotResult reports a captured screenshot.
type ShotResult struct {
	Case   string `json:"case"`
	Ticks  int    `json:"ticks"`
	Output string `json:"output"`
	Bytes  int64  `json:"bytes"`
}

// Cases returns the project's declared visual cases.
func Cases(ctx context.Context, sess *browser.Session) ([]Case, error) {
	var cases []Case
	if err := sess.EvalJSON(ctx, "JSON.stringify(window.__gameForge.visual.list())", &cases); err != nil {
		return nil, err
	}
	return cases, nil
}

// Shot loads a visual case, advances deterministically, and captures a PNG.
//
// A fresh page error fails the operation even if a PNG was written.
func Shot(ctx context.Context, sess *browser.Session, opts ShotOptions) (*ShotResult, error) {
	if opts.Case == "" {
		return nil, fmt.Errorf("shot: case is required")
	}
	if opts.Output == "" {
		return nil, fmt.Errorf("shot: output path is required")
	}
	_ = sess.P.ClearErrors(ctx)

	if err := loadCase(ctx, sess, opts.Case, opts.Seed, opts.WorldSeed); err != nil {
		return nil, err
	}
	if err := advance(ctx, sess, opts.Ticks); err != nil {
		return nil, err
	}
	if opts.Expr != "" {
		if _, err := sess.EvalString(ctx, opts.Expr); err != nil {
			return nil, fmt.Errorf("shot expr: %w", err)
		}
	}

	capturePath := opts.Output
	region := strings.ToLower(opts.Region)
	cropped := region != "" && region != "full"
	if cropped {
		capturePath = opts.Output + ".full.png"
	}
	if err := sess.P.Screenshot(ctx, capturePath); err != nil {
		return nil, fmt.Errorf("screenshot %s: %w", opts.Case, err)
	}
	if errs := freshErrors(ctx, sess); len(errs) > 0 {
		return nil, fmt.Errorf("page error during shot %s: %s", opts.Case, strings.Join(errs, "; "))
	}
	if cropped {
		rect, err := regionRect(ctx, sess, opts.Region)
		if err != nil {
			return nil, err
		}
		if err := cropPNG(capturePath, opts.Output, rect); err != nil {
			return nil, err
		}
		_ = os.Remove(capturePath)
	}
	info, err := os.Stat(opts.Output)
	if err != nil {
		return nil, fmt.Errorf("screenshot %s was not written: %w", opts.Case, err)
	}
	return &ShotResult{Case: opts.Case, Ticks: opts.Ticks, Output: opts.Output, Bytes: info.Size()}, nil
}

// RegionRect is a named crop rectangle in CSS pixels.
type RegionRect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

func regionRect(ctx context.Context, sess *browser.Session, name string) (RegionRect, error) {
	nameJSON, _ := json.Marshal(name)
	var rect RegionRect
	if err := sess.EvalJSON(ctx, fmt.Sprintf("JSON.stringify(window.__gameForge.region(%s))", nameJSON), &rect); err != nil {
		return rect, fmt.Errorf("resolve region %q: %w", name, err)
	}
	return rect, nil
}

// SweepResult reports one swept case.
type SweepResult struct {
	Case string `json:"case"`
	Kind string `json:"kind"`
	OK   bool   `json:"ok"`
	Err  string `json:"error,omitempty"`
}

// Sweep loads every declared visual case, advances, and reports fresh errors.
//
// Game Forge owns the iteration, browser lifecycle and reporting; the project
// declares the cases.
func Sweep(ctx context.Context, sess *browser.Session, ticks int) ([]SweepResult, error) {
	cases, err := Cases(ctx, sess)
	if err != nil {
		return nil, err
	}
	out := make([]SweepResult, 0, len(cases))
	for _, c := range cases {
		res := SweepResult{Case: c.Name, Kind: c.Kind}
		if err := sweepOne(ctx, sess, c.Name, ticks); err != nil {
			res.Err = err.Error()
		} else {
			res.OK = true
		}
		out = append(out, res)
	}
	return out, nil
}

func sweepOne(ctx context.Context, sess *browser.Session, name string, ticks int) error {
	_ = sess.P.ClearErrors(ctx)
	if err := loadCase(ctx, sess, name, "", ""); err != nil {
		return err
	}
	if err := advance(ctx, sess, ticks); err != nil {
		return err
	}
	if errs := freshErrors(ctx, sess); len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// loadCase arranges a visual case on the page.
func loadCase(ctx context.Context, sess *browser.Session, name, seed, world string) error {
	opts := map[string]any{}
	if seed != "" {
		opts["seed"] = seed
	}
	if world != "" {
		opts["worldSeed"] = world
	}
	optJSON, _ := json.Marshal(opts)
	nameJSON, _ := json.Marshal(name)
	js := fmt.Sprintf("window.__gameForge.visual.load(%s, %s); 'ok'", nameJSON, optJSON)
	if _, err := sess.EvalString(ctx, js); err != nil {
		return fmt.Errorf("load visual case %q: %w", name, err)
	}
	return nil
}

// advance steps the simulation deterministically.
func advance(ctx context.Context, sess *browser.Session, ticks int) error {
	if ticks <= 0 {
		return nil
	}
	if _, err := sess.EvalString(ctx, fmt.Sprintf("window.__gameForge.advance(%d); 'ok'", ticks)); err != nil {
		return fmt.Errorf("advance %d: %w", ticks, err)
	}
	return nil
}

// freshErrors returns accumulated page errors, ignoring the "no errors" marker.
func freshErrors(ctx context.Context, sess *browser.Session) []string {
	raw, err := sess.P.PageErrors(ctx)
	if err != nil {
		return nil
	}
	return FilterNoise(raw)
}

// FilterNoise drops non-error markers such as "No page errors".
func FilterNoise(msgs []string) []string {
	var out []string
	for _, m := range msgs {
		low := strings.ToLower(strings.TrimSpace(m))
		if low == "" || strings.Contains(low, "no page errors") || strings.Contains(low, "no console") {
			continue
		}
		out = append(out, m)
	}
	return out
}
