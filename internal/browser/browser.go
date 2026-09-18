// Package browser is Game Forge's browser-provider abstraction.
//
// Game projects never see this package. A project exposes the game-forge/v1
// browser contract (window.__gameForge); Game Forge owns how a real browser is
// launched, driven and torn down. Swapping providers (agent-browser today,
// something else later) must not require any game change.
package browser

import (
	"context"
	"encoding/json"
	"fmt"
)

// Provider drives a real browser for Game Forge.
//
// Implementations must be safe to call with a bounded context: every operation
// should return or fail rather than block indefinitely.
type Provider interface {
	// Name identifies the provider, e.g. "agent-browser".
	Name() string
	// Check validates that the provider is installed and can launch a browser.
	Check(ctx context.Context) (*CheckResult, error)
	// Open navigates the provider's session to url, launching the browser if
	// necessary.
	Open(ctx context.Context, url string) error
	// Eval runs JavaScript in the page and returns the raw JSON result.
	Eval(ctx context.Context, js string) (json.RawMessage, error)
	// Screenshot writes a PNG of the current viewport to path.
	Screenshot(ctx context.Context, path string) error
	// Close releases the provider session and its browser resources.
	Close(ctx context.Context) error
}

// CheckResult reports provider health.
type CheckResult struct {
	Provider string   `json:"provider"`
	Host     string   `json:"host"`
	OK       bool     `json:"ok"`
	Version  string   `json:"version,omitempty"`
	Chrome   string   `json:"chrome,omitempty"`
	LaunchMS int64    `json:"launch_ms,omitempty"`
	Details  []string `json:"details,omitempty"`
	Problems []string `json:"problems,omitempty"`
}

// AddDetail records an informational line.
func (c *CheckResult) AddDetail(format string, args ...any) {
	c.Details = append(c.Details, fmt.Sprintf(format, args...))
}

// AddProblem records a failure line and marks the result not OK.
func (c *CheckResult) AddProblem(format string, args ...any) {
	c.Problems = append(c.Problems, fmt.Sprintf(format, args...))
	c.OK = false
}
