// Package scenario orchestrates the project's authoritative scenarios.
//
// Scenario semantics live in the game. Game Forge discovers them through the
// declared adapter (headless) or the browser bridge, transports parameters and
// seeds generically, and normalizes the result. It never interprets scenario
// meaning: the report is opaque contract data.
package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/rceman/game-forge/internal/adapter"
	"github.com/rceman/game-forge/internal/browser"
)

// ParamSpec describes one tunable scenario parameter.
type ParamSpec struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Default string `json:"default"`
	Min     *int   `json:"min,omitempty"`
	Max     *int   `json:"max,omitempty"`
	Help    string `json:"help,omitempty"`
}

// Summary is the catalogue entry for a scenario.
type Summary struct {
	ID      string      `json:"id"`
	Summary string      `json:"summary"`
	Params  []ParamSpec `json:"params"`
}

// Check is one behavioural assertion reported by the game.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Report is the normalized result of running a scenario.
type Report struct {
	Scenario  string         `json:"scenario"`
	Status    string         `json:"status"`
	SimSeed   int64          `json:"simSeed"`
	WorldSeed int64          `json:"worldSeed"`
	Ticks     int            `json:"ticks"`
	Params    map[string]any `json:"params"`
	Checks    []Check        `json:"checks"`
	Metrics   map[string]any `json:"metrics"`
	Digest    string         `json:"digest"`
}

// Passed reports whether every check passed.
func (r *Report) Passed() bool { return r.Status == "PASS" && failedChecks(r) == 0 }

func failedChecks(r *Report) int {
	n := 0
	for _, c := range r.Checks {
		if !c.OK {
			n++
		}
	}
	return n
}

// RunOptions are the generic inputs Game Forge transports.
type RunOptions struct {
	ID        string
	Seed      string
	WorldSeed string
	Ticks     *int
	Params    map[string]string
}

// wireOptions renders the options object passed to the adapter/bridge.
func (o RunOptions) wireOptions() map[string]any {
	out := map[string]any{}
	if o.Seed != "" {
		out["seed"] = o.Seed
	}
	if o.WorldSeed != "" {
		out["worldSeed"] = o.WorldSeed
	}
	if o.Ticks != nil {
		out["ticks"] = *o.Ticks
	}
	if len(o.Params) > 0 {
		params := make(map[string]string, len(o.Params))
		for k, v := range o.Params {
			params[k] = v
		}
		out["params"] = params
	}
	return out
}

// List returns the scenario catalogue.
func List(ctx context.Context, client *adapter.Client) ([]Summary, error) {
	raw, err := client.Call(ctx, "scenario.list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var res struct {
		Scenarios []Summary `json:"scenarios"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("parse scenario.list: %w", err)
	}
	return res.Scenarios, nil
}

// Describe returns a scenario's full declaration.
func Describe(ctx context.Context, client *adapter.Client, id string) (json.RawMessage, error) {
	return client.Call(ctx, "scenario.describe", map[string]any{"id": id})
}

// Run executes a scenario headlessly through the adapter.
func Run(ctx context.Context, client *adapter.Client, opts RunOptions) (*Report, error) {
	params := map[string]any{"id": opts.ID}
	for k, v := range opts.wireOptions() {
		params[k] = v
	}
	raw, err := client.Call(ctx, "scenario.run", params)
	if err != nil {
		return nil, err
	}
	return decodeReport(raw)
}

// RunInBrowser executes the SAME scenario through the browser bridge.
func RunInBrowser(ctx context.Context, sess *browser.Session, opts RunOptions) (*Report, error) {
	wire, err := json.Marshal(opts.wireOptions())
	if err != nil {
		return nil, err
	}
	idJSON, err := json.Marshal(opts.ID)
	if err != nil {
		return nil, err
	}
	js := fmt.Sprintf("JSON.stringify(window.__gameForge.scenarioRun(%s, %s))", idJSON, wire)
	str, err := sess.EvalString(ctx, js)
	if err != nil {
		return nil, err
	}
	return decodeReport(json.RawMessage(str))
}

func decodeReport(raw json.RawMessage) (*Report, error) {
	rep := &Report{}
	if err := json.Unmarshal(raw, rep); err != nil {
		return nil, fmt.Errorf("parse scenario report: %w (%.200s)", err, raw)
	}
	return rep, nil
}

// CompareResult reports whether two reports agree on authoritative output.
type CompareResult struct {
	ID          string `json:"id"`
	Match       bool   `json:"match"`
	Digest      string `json:"digest"`
	Headless    string `json:"headlessDigest"`
	Browser     string `json:"browserDigest"`
	HeadlessOK  bool   `json:"headlessPass"`
	BrowserOK   bool   `json:"browserPass"`
	Explanation string `json:"explanation,omitempty"`
}

// Compare runs the same scenario headlessly and in the browser and compares the
// authoritative digest and per-check results.
func Compare(ctx context.Context, client *adapter.Client, sess *browser.Session, opts RunOptions) (*CompareResult, error) {
	headless, err := Run(ctx, client, opts)
	if err != nil {
		return nil, fmt.Errorf("headless: %w", err)
	}
	browserRep, err := RunInBrowser(ctx, sess, opts)
	if err != nil {
		return nil, fmt.Errorf("browser: %w", err)
	}
	res := &CompareResult{
		ID:         opts.ID,
		HeadlessOK: headless.Passed(),
		BrowserOK:  browserRep.Passed(),
		Headless:   headless.Digest,
		Browser:    browserRep.Digest,
		Digest:     headless.Digest,
	}
	res.Match = headless.Digest == browserRep.Digest && res.HeadlessOK == res.BrowserOK && checksEqual(headless.Checks, browserRep.Checks)
	if !res.Match {
		res.Explanation = explain(headless, browserRep)
	}
	return res, nil
}

func checksEqual(a, b []Check) bool {
	if len(a) != len(b) {
		return false
	}
	am := map[string]bool{}
	for _, c := range a {
		am[c.Name] = c.OK
	}
	for _, c := range b {
		if got, ok := am[c.Name]; !ok || got != c.OK {
			return false
		}
	}
	return true
}

func explain(a, b *Report) string {
	if a.Digest != b.Digest {
		return fmt.Sprintf("digest mismatch: headless=%s browser=%s", a.Digest, b.Digest)
	}
	if a.Passed() != b.Passed() {
		return fmt.Sprintf("check status mismatch: headless=%s browser=%s", a.Status, b.Status)
	}
	return "check results differ"
}

// FormatReport renders a compact human-readable report.
func FormatReport(r *Report, verbose bool) string {
	out := fmt.Sprintf("%s scenario=%s\n", r.Status, r.Scenario)
	out += fmt.Sprintf("simSeed=%d worldSeed=%d ticks=%d\n", r.SimSeed, r.WorldSeed, r.Ticks)
	if len(r.Params) > 0 {
		keys := make([]string, 0, len(r.Params))
		for k := range r.Params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out += fmt.Sprintf("%s=%v ", k, r.Params[k])
		}
		out += "\n"
	}
	for _, c := range r.Checks {
		if verbose || !c.OK {
			mark := "ok"
			if !c.OK {
				mark = "FAIL"
			}
			out += fmt.Sprintf("  %s %s: %s\n", mark, c.Name, c.Detail)
		}
	}
	out += fmt.Sprintf("digest=%s", r.Digest)
	return out
}
