// Package gpu verifies the browser's real GPU path and drives the project's
// own benchmark semantics.
//
// Game Forge owns the browser orchestration, renderer validation, warmup,
// timing, reporting and cleanup. The game owns the benchmark scenario/state and
// its render metrics; Game Forge does not invent a performance budget.
package gpu

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rceman/game-forge/internal/browser"
)

// Benchmark is the project's reported benchmark result.
type Benchmark struct {
	Frames      int     `json:"frames"`
	MeanFrameMs float64 `json:"meanFrameMs"`
	DrawCalls   int     `json:"drawCalls"`
	Triangles   int     `json:"triangles"`
	Textures    int     `json:"textures"`
	Enemies     int     `json:"enemies"`
	Projectiles int     `json:"projectiles"`
}

// Result reports a GPU verification run.
type Result struct {
	Renderer  browser.RendererInfo `json:"renderer"`
	Software  bool                 `json:"software"`
	Scenario  string               `json:"scenario"`
	Ticks     int                  `json:"ticks"`
	Benchmark *Benchmark           `json:"benchmark,omitempty"`
	OK        bool                 `json:"ok"`
}

// Options configure a GPU verification run.
type Options struct {
	Scenario string
	Ticks    int
	Frames   int
}

// Verify inspects the WebGL renderer, rejects software fallbacks, and drives
// the project's benchmark scenario.
func Verify(ctx context.Context, sess *browser.Session, opts Options) (*Result, error) {
	info, err := sess.P.Renderer(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect webgl renderer: %w", err)
	}
	res := &Result{
		Renderer: *info,
		Software: info.IsSoftware(),
		Scenario: opts.Scenario,
		Ticks:    opts.Ticks,
	}
	if res.Software {
		return res, fmt.Errorf("software renderer detected: %s", info.UnmaskedRenderer)
	}

	if opts.Scenario != "" {
		nameJSON, _ := json.Marshal(opts.Scenario)
		if _, err := sess.EvalString(ctx, fmt.Sprintf("window.__gameForge.visual.load(%s); 'ok'", nameJSON)); err != nil {
			return res, fmt.Errorf("load benchmark scenario %q: %w", opts.Scenario, err)
		}
		if opts.Ticks > 0 {
			if _, err := sess.EvalString(ctx, fmt.Sprintf("window.__gameForge.advance(%d); 'ok'", opts.Ticks)); err != nil {
				return res, fmt.Errorf("advance %d: %w", opts.Ticks, err)
			}
		}
	}

	frames := opts.Frames
	if frames <= 0 {
		frames = 120
	}
	var bench Benchmark
	if err := sess.EvalJSON(ctx, fmt.Sprintf("JSON.stringify(window.__gameForge.benchmark(%d))", frames), &bench); err != nil {
		return res, fmt.Errorf("benchmark: %w", err)
	}
	res.Benchmark = &bench
	res.OK = true
	return res, nil
}
