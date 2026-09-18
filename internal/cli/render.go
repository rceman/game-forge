package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// renderFunc renders an operation result as human output and returns the
// command's exit code. It receives the operation's data payload.
type renderFunc func(w io.Writer, data json.RawMessage) int

// decode unmarshals a data payload; nil data decodes as an empty object.
func decode(data json.RawMessage, v any) error {
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}

func renderJSON(w io.Writer, data json.RawMessage) int {
	var v any
	if err := decode(data, &v); err != nil {
		fmt.Fprintln(w, string(data))
		return ExitOK
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return ExitOK
}

// renderProjectInfo → project.info
func renderProjectInfo(w io.Writer, data json.RawMessage) int {
	var d struct {
		Root         string   `json:"root"`
		ID           string   `json:"id"`
		Type         string   `json:"type"`
		Contract     string   `json:"contract"`
		Capabilities []string `json:"capabilities"`
		Adapters     []struct {
			Name     string   `json:"name"`
			Command  []string `json:"command"`
			Protocol string   `json:"protocol"`
		} `json:"adapters"`
	}
	if err := decode(data, &d); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge project info: %v\n", err)
		return ExitFail
	}
	fmt.Fprintln(w, "game-forge project")
	fmt.Fprintf(w, "  root:       %s\n", d.Root)
	fmt.Fprintf(w, "  id:         %s\n", d.ID)
	fmt.Fprintf(w, "  type:       %s\n", d.Type)
	fmt.Fprintf(w, "  contract:   %s\n", d.Contract)
	if len(d.Capabilities) > 0 {
		sort.Strings(d.Capabilities)
		fmt.Fprintf(w, "  capabilities: %s\n", strings.Join(d.Capabilities, ", "))
	}
	if len(d.Adapters) > 0 {
		fmt.Fprintln(w, "  adapters:")
		for _, a := range d.Adapters {
			fmt.Fprintf(w, "    - %s: %s (%s)\n", a.Name, strings.Join(a.Command, " "), a.Protocol)
		}
	}
	return ExitOK
}

// renderDoctor → doctor.run
func renderDoctor(w io.Writer, data json.RawMessage) int {
	var d struct {
		OK             bool     `json:"ok"`
		ConfigPath     string   `json:"configPath"`
		ConfigExists   bool     `json:"configExists"`
		Namespace      string   `json:"namespace"`
		Provider       string   `json:"provider"`
		Host           string   `json:"host"`
		Details        []string `json:"details"`
		Problems       []string `json:"problems"`
		LeftRegistered bool     `json:"leftRegistered"`
		LaunchArgs     string   `json:"launchArgs"`
	}
	if err := decode(data, &d); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge doctor: %v\n", err)
		return ExitFail
	}
	fmt.Fprintln(w, "game-forge doctor")
	state := "not found (using defaults)"
	if d.ConfigExists {
		state = "found"
	}
	fmt.Fprintf(w, "  config:   %s (%s)\n", d.ConfigPath, state)
	fmt.Fprintf(w, "  provider: %s (host: %s)\n", d.Provider, d.Host)
	for _, x := range d.Details {
		fmt.Fprintf(w, "    - %s\n", x)
	}
	if d.LeftRegistered {
		fmt.Fprintf(w, "  resource: left registered (namespace %s) for gc\n", d.Namespace)
	} else {
		fmt.Fprintln(w, "  resource: registered then released (normal cleanup)")
	}
	if len(d.Problems) > 0 {
		fmt.Fprintln(w, "  problems:")
		for _, p := range d.Problems {
			fmt.Fprintf(w, "    ! %s\n", p)
		}
		fmt.Fprintln(w, "  status:   FAIL")
		return ExitFail
	}
	fmt.Fprintln(w, "  status:   OK")
	return ExitOK
}

// renderScenarioList → scenario.list
func renderScenarioList(w io.Writer, data json.RawMessage) int {
	var d struct {
		Scenarios []struct {
			ID      string `json:"id"`
			Summary string `json:"summary"`
			Params  []struct {
				Name    string `json:"name"`
				Default string `json:"default"`
				Min     *int   `json:"min"`
				Max     *int   `json:"max"`
				Help    string `json:"help"`
			} `json:"params"`
		} `json:"scenarios"`
	}
	if err := decode(data, &d); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario list: %v\n", err)
		return ExitFail
	}
	for _, s := range d.Scenarios {
		fmt.Fprintf(w, "%-22s %s\n", s.ID, s.Summary)
		for _, p := range s.Params {
			bounds := ""
			if p.Min != nil || p.Max != nil {
				bounds = fmt.Sprintf(" [%v..%v]", derefInt(p.Min), derefInt(p.Max))
			}
			fmt.Fprintf(w, "  --%s=%s%s  %s\n", p.Name, p.Default, bounds, p.Help)
		}
	}
	return ExitOK
}

// renderScenarioRun → scenario.run
func renderScenarioRun(w io.Writer, data json.RawMessage) int {
	var d struct {
		Scenario  string `json:"scenario"`
		Passed    bool   `json:"passed"`
		Status    string `json:"status"`
		Digest    string `json:"digest"`
		SimSeed   int64  `json:"simSeed"`
		WorldSeed int64  `json:"worldSeed"`
		Ticks     int    `json:"ticks"`
		Browser   bool   `json:"browser"`
		Checks    []struct {
			Name   string `json:"name"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := decode(data, &d); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario run: %v\n", err)
		return ExitFail
	}
	for _, ch := range d.Checks {
		mark := "PASS"
		if !ch.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(w, "%-5s %-28s %s\n", mark, ch.Name, ch.Detail)
	}
	fmt.Fprintf(w, "scenario=%s status=%s digest=%s ticks=%d simSeed=%d worldSeed=%d\n",
		d.Scenario, d.Status, d.Digest, d.Ticks, d.SimSeed, d.WorldSeed)
	if !d.Passed {
		return ExitFail
	}
	return ExitOK
}

// renderScenarioCompare → scenario.compare
func renderScenarioCompare(w io.Writer, data json.RawMessage) int {
	var d struct {
		Compared  int `json:"compared"`
		Differing int `json:"differing"`
		Results   []struct {
			ID          string `json:"id"`
			Match       bool   `json:"match"`
			Headless    string `json:"headless"`
			Browser     string `json:"browser"`
			Digest      string `json:"digest"`
			Explanation string `json:"explanation"`
		} `json:"results"`
	}
	if err := decode(data, &d); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario compare: %v\n", err)
		return ExitFail
	}
	for _, r := range d.Results {
		mark := "MATCH"
		if !r.Match {
			mark = "DIFF"
		}
		fmt.Fprintf(w, "%-6s %-22s headless=%s browser=%s digest=%s\n", mark, r.ID, r.Headless, r.Browser, r.Digest)
		if !r.Match {
			fmt.Fprintf(w, "       %s\n", r.Explanation)
		}
	}
	fmt.Fprintf(w, "compared %d scenarios, %d differing\n", d.Compared, d.Differing)
	if d.Differing > 0 {
		return ExitFail
	}
	return ExitOK
}

// renderShot → visual.shot
func renderShot(w io.Writer, data json.RawMessage) int {
	var d struct {
		Case   string `json:"case"`
		Ticks  int    `json:"ticks"`
		Region string `json:"region"`
		Output string `json:"output"`
		Bytes  int64  `json:"bytes"`
	}
	if err := decode(data, &d); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge shot: %v\n", err)
		return ExitFail
	}
	fmt.Fprintf(w, "case=%s ticks=%d region=%s\nscreenshot=%s (%d bytes)\n", d.Case, d.Ticks, d.Region, d.Output, d.Bytes)
	return ExitOK
}

// renderSweep → visual.sweep
func renderSweep(w io.Writer, data json.RawMessage) int {
	var d struct {
		Failed int `json:"failed"`
		Cases  []struct {
			Case string `json:"case"`
			Kind string `json:"kind"`
			OK   bool   `json:"ok"`
			Err  string `json:"error"`
		} `json:"cases"`
	}
	if err := decode(data, &d); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge sweep: %v\n", err)
		return ExitFail
	}
	for _, r := range d.Cases {
		mark := "ok"
		if !r.OK {
			mark = "FAIL"
		}
		line := fmt.Sprintf("%-26s %-8s %s", r.Case, r.Kind, mark)
		if r.Err != "" {
			line += ": " + r.Err
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintf(w, "swept %d cases, %d failed\n", len(d.Cases), d.Failed)
	if d.Failed > 0 {
		return ExitFail
	}
	return ExitOK
}

// renderPixel → visual.pixel
func renderPixel(w io.Writer, data json.RawMessage) int {
	var d struct {
		Image   string `json:"image"`
		Width   int    `json:"width"`
		Height  int    `json:"height"`
		Samples []struct {
			X, Y       int
			R, G, B, A int
			Color      string
		} `json:"samples"`
	}
	if err := decode(data, &d); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge pixel: %v\n", err)
		return ExitFail
	}
	fmt.Fprintf(w, "image=%s size=%dx%d\n", d.Image, d.Width, d.Height)
	for _, s := range d.Samples {
		fmt.Fprintf(w, "(%d,%d) = (%d, %d, %d)\n", s.X, s.Y, s.R, s.G, s.B)
	}
	return ExitOK
}

// renderEval → browser.eval
func renderEval(w io.Writer, data json.RawMessage) int {
	var d struct {
		Value any `json:"value"`
	}
	if err := decode(data, &d); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge eval: %v\n", err)
		return ExitFail
	}
	if s, ok := d.Value.(string); ok {
		fmt.Fprintln(w, s)
		return ExitOK
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(d.Value)
	return ExitOK
}

// renderErrors → browser.errors
func renderErrors(w io.Writer, data json.RawMessage) int {
	var d struct {
		OK       bool     `json:"ok"`
		Messages []string `json:"messages"`
	}
	if err := decode(data, &d); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge errors: %v\n", err)
		return ExitFail
	}
	if d.OK {
		fmt.Fprintln(w, "PASS no page/console diagnostics")
		return ExitOK
	}
	for _, m := range d.Messages {
		fmt.Fprintln(w, m)
	}
	return ExitFail
}

// renderProfileSummary → profile.run. Per-stage lines are shown live from the
// event stream; the result needs only the summary line.
func renderProfileSummary(w io.Writer, data json.RawMessage) int {
	var d struct {
		Profile string `json:"profile"`
		OK      bool   `json:"ok"`
		MS      int64  `json:"ms"`
		Stages  []struct {
			Name   string `json:"name"`
			Output string `json:"output"`
		} `json:"stages"`
	}
	if err := decode(data, &d); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge %s: %v\n", d.Profile, err)
		return ExitFail
	}
	// A stage's full output is only printed when explicitly requested
	// (--verbose) — and stages were already streamed, so print it inline here.
	for _, s := range d.Stages {
		if s.Output != "" {
			for _, line := range strings.Split(strings.TrimRight(s.Output, "\n"), "\n") {
				fmt.Fprintf(w, "      %s\n", line)
			}
		}
	}
	fmt.Fprintf(w, "%s profile=%s stages=%d duration=%s\n", boolWord(d.OK, "PASS", "FAIL"), d.Profile, len(d.Stages), fmtMS(d.MS))
	if !d.OK {
		return ExitFail
	}
	return ExitOK
}

// renderServerStart → server.start
func renderServerStart(w io.Writer, data json.RawMessage) int {
	var d struct {
		Server  string `json:"server"`
		State   string `json:"state"`
		URL     string `json:"url"`
		PID     int    `json:"pid"`
		Log     string `json:"log"`
		LeaseMs int64  `json:"leaseMs"`
	}
	if err := decode(data, &d); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge serve: %v\n", err)
		return ExitFail
	}
	fmt.Fprintf(w, "server=%s state=%s url=%s\n", d.Server, d.State, d.URL)
	if d.State == "started" {
		fmt.Fprintf(w, "pid=%d log=%s lease=%s\n", d.PID, d.Log, fmtMS(d.LeaseMs))
		fmt.Fprintln(w, "registered as an owned resource; stop it with 'game-forge serve stop'")
	} else {
		fmt.Fprintln(w, "already reachable, so it was left alone (not owned by this run)")
	}
	return ExitOK
}

// renderServerState → server.status / server.stop
func renderServerState(w io.Writer, data json.RawMessage) int {
	var d struct {
		Server string `json:"server"`
		State  string `json:"state"`
		URL    string `json:"url"`
		Owned  bool   `json:"owned"`
		ID     string `json:"id"`
		PID    int    `json:"pid"`
	}
	if err := decode(data, &d); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge serve: %v\n", err)
		return ExitFail
	}
	fmt.Fprintf(w, "server=%s state=%s url=%s\n", d.Server, d.State, d.URL)
	if d.PID > 0 {
		fmt.Fprintf(w, "  id=%s pid=%d\n", d.ID, d.PID)
	}
	return ExitOK
}

// renderResourceList → resource.list
func renderResourceList(w io.Writer, data json.RawMessage) int {
	var d struct {
		Resources []struct {
			ID        string `json:"id"`
			Kind      string `json:"kind"`
			Provider  string `json:"provider"`
			Namespace string `json:"namespace"`
			State     string `json:"state"`
		} `json:"resources"`
	}
	if err := decode(data, &d); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge ps: %v\n", err)
		return ExitFail
	}
	if len(d.Resources) == 0 {
		fmt.Fprintln(w, "no owned resources")
		return ExitOK
	}
	fmt.Fprintf(w, "%-28s %-8s %-14s %-24s %-8s\n", "ID", "KIND", "PROVIDER", "NAMESPACE", "STATE")
	for _, r := range d.Resources {
		fmt.Fprintf(w, "%-28s %-8s %-14s %-24s %-8s\n", r.ID, r.Kind, r.Provider, r.Namespace, r.State)
	}
	return ExitOK
}

// renderReclaim → resource.gc / resource.tick
func renderReclaim(w io.Writer, data json.RawMessage) int {
	var d struct {
		Reclaimed []struct {
			ID        string `json:"id"`
			Kind      string `json:"kind"`
			Provider  string `json:"provider"`
			Namespace string `json:"namespace"`
		} `json:"reclaimed"`
		Failed int `json:"failed"`
	}
	if err := decode(data, &d); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge: %v\n", err)
		return ExitFail
	}
	if len(d.Reclaimed) == 0 {
		fmt.Fprintln(w, "nothing to reclaim")
		return ExitOK
	}
	for _, r := range d.Reclaimed {
		fmt.Fprintf(w, "reclaiming %s (%s %s namespace=%s)\n", r.ID, r.Provider, r.Kind, r.Namespace)
	}
	if d.Failed > 0 {
		return ExitFail
	}
	return ExitOK
}

// renderGPU → gpu.run
func renderGPU(w io.Writer, data json.RawMessage) int {
	var d struct {
		OK       bool   `json:"ok"`
		Software bool   `json:"software"`
		Scenario string `json:"scenario"`
		Renderer struct {
			Vendor           string `json:"vendor"`
			UnmaskedVendor   string `json:"unmaskedVendor"`
			Renderer         string `json:"renderer"`
			UnmaskedRenderer string `json:"unmaskedRenderer"`
		} `json:"renderer"`
		Benchmark *struct {
			Frames      int     `json:"frames"`
			MeanFrameMs float64 `json:"meanFrameMs"`
			DrawCalls   int     `json:"drawCalls"`
			Triangles   int     `json:"triangles"`
			Textures    int     `json:"textures"`
			Enemies     int     `json:"enemies"`
			Projectiles int     `json:"projectiles"`
		} `json:"benchmark"`
	}
	if err := decode(data, &d); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge gpu: %v\n", err)
		return ExitFail
	}
	fmt.Fprintln(w, "game-forge gpu")
	fmt.Fprintf(w, "  vendor:            %s\n", d.Renderer.Vendor)
	fmt.Fprintf(w, "  unmasked vendor:   %s\n", d.Renderer.UnmaskedVendor)
	fmt.Fprintf(w, "  renderer:          %s\n", d.Renderer.Renderer)
	fmt.Fprintf(w, "  unmasked renderer: %s\n", d.Renderer.UnmaskedRenderer)
	fmt.Fprintf(w, "  software:          %v\n", d.Software)
	if d.Benchmark != nil {
		b := d.Benchmark
		fmt.Fprintf(w, "  benchmark:         scenario=%s frames=%d meanFrameMs=%.2f draws=%d tris=%d textures=%d enemies=%d projectiles=%d\n",
			d.Scenario, b.Frames, b.MeanFrameMs, b.DrawCalls, b.Triangles, b.Textures, b.Enemies, b.Projectiles)
	}
	if !d.OK {
		return ExitFail
	}
	return ExitOK
}

// ---- helpers ----------------------------------------------------------------

func derefInt(p *int) any {
	if p == nil {
		return "*"
	}
	return *p
}

func boolWord(ok bool, t, f string) string {
	if ok {
		return t
	}
	return f
}

// fmtMS renders milliseconds as a compact duration.
func fmtMS(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	s := ms / 1000
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}
