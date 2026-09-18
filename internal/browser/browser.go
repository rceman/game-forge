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
	"strings"
	"time"
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
	// Navigate points the session at url.
	Navigate(ctx context.Context, url string) error
	// Reload reloads the current page.
	Reload(ctx context.Context) error
	// SetViewport sets the browser viewport in CSS pixels.
	SetViewport(ctx context.Context, width, height int) error
	// Eval runs JavaScript in the page and returns the raw JSON result.
	Eval(ctx context.Context, js string) (json.RawMessage, error)
	// Click clicks the element matching a selector.
	Click(ctx context.Context, selector string) error
	// Press sends a key press.
	Press(ctx context.Context, key string) error
	// Screenshot writes a PNG of the current viewport to path.
	Screenshot(ctx context.Context, path string) error
	// PageErrors returns accumulated uncaught page errors.
	PageErrors(ctx context.Context) ([]string, error)
	// ConsoleErrors returns accumulated console output.
	ConsoleErrors(ctx context.Context) ([]string, error)
	// ClearErrors drains accumulated page errors so an operation can assert
	// that it produced no fresh ones.
	ClearErrors(ctx context.Context) error
	// Renderer reports the active WebGL renderer.
	Renderer(ctx context.Context) (*RendererInfo, error)
	// Close releases the provider session and its browser resources.
	Close(ctx context.Context) error
}

// RendererInfo describes the WebGL renderer backing the browser.
type RendererInfo struct {
	Vendor           string `json:"vendor"`
	UnmaskedVendor   string `json:"unmaskedVendor"`
	Renderer         string `json:"renderer"`
	UnmaskedRenderer string `json:"unmaskedRenderer"`
}

// IsSoftware reports whether the renderer looks like a software fallback.
func (r *RendererInfo) IsSoftware() bool {
	haystack := strings.ToLower(strings.Join([]string{r.Vendor, r.UnmaskedVendor, r.Renderer, r.UnmaskedRenderer}, " "))
	for _, marker := range []string{"swiftshader", "warp", "llvmpipe", "microsoft basic render", "software"} {
		if strings.Contains(haystack, marker) {
			return true
		}
	}
	return false
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

// Session is a higher-level browser workflow over a Provider: it opens a page,
// waits for the project's game-forge/v1 bridge, and offers bounded operations.
// It contains no provider-specific knowledge.
type Session struct {
	P   Provider
	URL string
}

// OpenSession opens url and waits for the project bridge to become available.
func OpenSession(ctx context.Context, p Provider, url string, viewport [2]int) (*Session, error) {
	s, err := OpenSessionRaw(ctx, p, url, viewport)
	if err != nil {
		return nil, err
	}
	if err := WaitForBridge(ctx, p, 24); err != nil {
		// A stale page (server restarted, or a previous navigation) is
		// recoverable with one reload.
		_ = p.Reload(ctx)
		if err := WaitForBridge(ctx, p, 24); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// OpenSessionRaw opens url and sets the viewport WITHOUT waiting for the
// project bridge. Production builds deliberately do not expose the dev bridge,
// so the production smoke uses this.
func OpenSessionRaw(ctx context.Context, p Provider, url string, viewport [2]int) (*Session, error) {
	if err := p.Open(ctx, url); err != nil {
		return nil, err
	}
	if viewport[0] > 0 && viewport[1] > 0 {
		_ = p.SetViewport(ctx, viewport[0], viewport[1])
	}
	return &Session{P: p, URL: url}, nil
}

// WaitForBridge polls for window.__gameForge.
func WaitForBridge(ctx context.Context, p Provider, tries int) error {
	for i := 0; i < tries; i++ {
		if raw, err := p.Eval(ctx, "typeof window.__gameForge !== 'undefined'"); err == nil {
			var b bool
			if json.Unmarshal(raw, &b) == nil && b {
				return nil
			}
			var s string
			if json.Unmarshal(raw, &s) == nil && s == "true" {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("game-forge bridge (window.__gameForge) did not become available")
}

// EvalJSON evaluates js and decodes the JSON result into out.
//
// A JSON string result (the usual case for JSON.stringify(...)) is unwrapped
// before decoding.
func (s *Session) EvalJSON(ctx context.Context, js string, out any) error {
	raw, err := s.P.Eval(ctx, js)
	if err != nil {
		return err
	}
	raw = unwrapJSONString(raw)
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode eval result: %w (%.200s)", err, raw)
	}
	return nil
}

// EvalString evaluates js and returns a string result, unwrapping JSON strings.
func (s *Session) EvalString(ctx context.Context, js string) (string, error) {
	raw, err := s.P.Eval(ctx, js)
	if err != nil {
		return "", err
	}
	var sres string
	if json.Unmarshal(raw, &sres) == nil {
		return sres, nil
	}
	return strings.Trim(string(raw), "\""), nil
}

// Close releases the session's browser resources.
func (s *Session) Close(ctx context.Context) error { return s.P.Close(ctx) }
