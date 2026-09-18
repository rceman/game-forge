package core

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"time"

	"github.com/rceman/game-forge/internal/browser"
	"github.com/rceman/game-forge/internal/config"
	"github.com/rceman/game-forge/internal/gpu"
	"github.com/rceman/game-forge/internal/op"
	"github.com/rceman/game-forge/internal/process"
	"github.com/rceman/game-forge/internal/profile"
	"github.com/rceman/game-forge/internal/scenario"
	"github.com/rceman/game-forge/internal/server"
	"github.com/rceman/game-forge/internal/visual"
)

// Register wires every public operation into the registry. Each operation's
// canonical name, summary and handler are declared once here; the wire contract
// comes from the checked-in schemas.
func Register(r *op.Registry, rt *Runtime) error {
	for _, d := range []struct {
		name, summary string
		stream        bool
		h             op.Handler
	}{
		{"project.info", "Show the nearest project manifest", false, rt.projectInfo},
		{"doctor.run", "Validate machine config and the browser provider", true, rt.doctorRun},
		{"scenario.list", "List the project's scenarios", false, rt.scenarioList},
		{"scenario.describe", "Describe one scenario", false, rt.scenarioDescribe},
		{"scenario.run", "Run a scenario headlessly or in the browser", true, rt.scenarioRun},
		{"scenario.compare", "Compare headless and browser output", true, rt.scenarioCompare},
		{"visual.shot", "Deterministic screenshot of a visual case", true, rt.visualShot},
		{"visual.sweep", "Load every visual case and report failures", true, rt.visualSweep},
		{"visual.pixel", "Sample real rendered pixels", true, rt.visualPixel},
		{"browser.eval", "Evaluate a project expression in the browser", true, rt.browserEval},
		{"browser.errors", "Report page and console diagnostics", true, rt.browserErrors},
		{"profile.run", "Run a declared validation profile", true, rt.profileRun},
		{"server.start", "Ensure a declared server is running", true, rt.serverStart},
		{"server.status", "Report a declared server's state", false, rt.serverStatus},
		{"server.stop", "Stop a declared server", true, rt.serverStop},
		{"resource.list", "List owned resources", false, rt.resourceList},
		{"resource.gc", "Reclaim expired owned resources", true, rt.resourceGC},
		{"resource.tick", "One idempotent housekeeping pass", false, rt.resourceTick},
		{"gpu.run", "Verify the real GPU renderer and benchmark", true, rt.gpuRun},
	} {
		if err := r.Add(&op.Operation{Name: d.name, Summary: d.summary, Stream: d.stream, Handler: d.h}); err != nil {
			return err
		}
	}
	return nil
}

// core builds a per-request Core. The caller's cwd comes from the request
// context so the daemon's own directory never leaks into project discovery.
func (rt *Runtime) core(ctx context.Context) (*Core, error) {
	return newCore(rt, op.CwdFrom(ctx), op.RunIDFrom(ctx))
}

// toErr converts an arbitrary error into the structured wire error. *op.Error
// is preserved; everything else becomes a generic failure.
func toErr(err error) *op.Error {
	if err == nil {
		return nil
	}
	var e *op.Error
	if as(err, &e) {
		return e
	}
	return &op.Error{Code: op.CodeFailed, Msg: err.Error()}
}

// as is errors.As with a generic-friendly signature.
func as(err error, target **op.Error) bool {
	for err != nil {
		if e, ok := err.(*op.Error); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return false
}

// fail builds a structured failure from a code and formatted message.
func fail(code, format string, a ...any) *op.Error {
	return &op.Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// timeout returns a derived context bound for an operation.
func timeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}

// ---- project.info -----------------------------------------------------------

type projectInfoOut struct {
	Root         string        `json:"root"`
	ID           string        `json:"id"`
	Type         string        `json:"type"`
	Contract     string        `json:"contract"`
	Capabilities []string      `json:"capabilities"`
	Adapters     []adapterDesc `json:"adapters"`
}

type adapterDesc struct {
	Name     string   `json:"name"`
	Command  []string `json:"command"`
	Protocol string   `json:"protocol"`
}

func (rt *Runtime) projectInfo(ctx context.Context, _ json.RawMessage, _ op.Sink) (any, error) {
	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	m, err := c.manifest()
	if err != nil {
		return nil, toErr(err)
	}
	out := projectInfoOut{
		Root:         m.Root,
		ID:           m.Project.ID,
		Type:         m.Project.Type,
		Contract:     m.Contract,
		Capabilities: []string{},
		Adapters:     []adapterDesc{},
	}
	for k, v := range m.Capabilities {
		if v {
			out.Capabilities = append(out.Capabilities, k)
		}
	}
	for name, a := range m.Adapters {
		out.Adapters = append(out.Adapters, adapterDesc{Name: name, Command: a.Command, Protocol: a.Protocol})
	}
	return out, nil
}

// ---- doctor.run -------------------------------------------------------------

type doctorOut struct {
	OK             bool     `json:"ok"`
	ConfigPath     string   `json:"configPath"`
	ConfigExists   bool     `json:"configExists"`
	Namespace      string   `json:"namespace"`
	Provider       string   `json:"provider"`
	Host           string   `json:"host"`
	Details        []string `json:"details"`
	Problems       []string `json:"problems"`
	LeftRegistered bool     `json:"leftRegistered"`
	LaunchArgs     string   `json:"launchArgs,omitempty"`
}

func (rt *Runtime) doctorRun(ctx context.Context, raw json.RawMessage, sink op.Sink) (any, error) {
	var a struct {
		NoCleanup bool `json:"noCleanup"`
		LeaseMs   int  `json:"leaseMs"`
		TimeoutMs int  `json:"timeoutMs"`
		Unmuted   bool `json:"unmuted"`
	}
	_ = json.Unmarshal(raw, &a)
	cctx, cancel := timeout(ctx, orDuration(a.TimeoutMs, 90*time.Second))
	defer cancel()

	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	cfg, path, err := config.LoadDefault()
	if err != nil {
		return nil, toErr(err)
	}
	_, statErr := os.Stat(path)

	ns := c.namespaceFor(c.runID)
	res := &process.Resource{
		ID:        "browser-" + c.runID,
		RunID:     c.runID,
		Kind:      process.KindBrowser,
		Provider:  cfg.Browser.Provider,
		Namespace: ns,
		Host:      cfg.Browser.Host,
		Expires:   time.Now().UTC().Add(orDuration(a.LeaseMs, 5*time.Minute)),
	}
	if err := c.reg.Register(res); err != nil {
		return nil, fail(op.CodeFailed, "register resource: %v", err)
	}
	provider := browser.NewAgentBrowser(cfg, ns)
	if a.Unmuted || cfg.Browser.AgentBrowser.Unmuted {
		provider.SetUnmuted(true)
	}
	check, err := provider.Check(cctx)
	if err != nil {
		return nil, fail(op.CodeFailed, "provider check: %v", err)
	}
	if !a.NoCleanup {
		_ = provider.Close(context.Background())
		if err := c.reg.Remove(res.ID); err != nil {
			return nil, fail(op.CodeFailed, "remove resource: %v", err)
		}
	}
	out := doctorOut{
		OK:             check.OK,
		ConfigPath:     path,
		ConfigExists:   statErr == nil,
		Namespace:      ns,
		Provider:       check.Provider,
		Host:           check.Host,
		Details:        check.Details,
		Problems:       check.Problems,
		LeftRegistered: a.NoCleanup,
		LaunchArgs:     provider.LaunchArgs(),
	}
	if out.Details == nil {
		out.Details = []string{}
	}
	if out.Problems == nil {
		out.Problems = []string{}
	}
	out.OK = check.OK
	if !out.OK {
		return out, fail(op.CodeFailed, "doctor found %d problems", len(out.Problems))
	}
	return out, nil
}

// ---- scenario.* -------------------------------------------------------------

type scenarioRunArgs struct {
	ID      string            `json:"id"`
	Seed    string            `json:"seed"`
	World   string            `json:"world"`
	Ticks   int               `json:"ticks"`
	Browser bool              `json:"browser"`
	Unmuted bool              `json:"unmuted"`
	Params  map[string]string `json:"params"`
}

type checkOut struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type scenarioRunOut struct {
	Scenario  string     `json:"scenario"`
	Passed    bool       `json:"passed"`
	Status    string     `json:"status"`
	Digest    string     `json:"digest"`
	SimSeed   int64      `json:"simSeed"`
	WorldSeed int64      `json:"worldSeed"`
	Ticks     int        `json:"ticks"`
	Browser   bool       `json:"browser"`
	Checks    []checkOut `json:"checks"`
}

func (rt *Runtime) scenarioList(ctx context.Context, _ json.RawMessage, _ op.Sink) (any, error) {
	cctx, cancel := timeout(ctx, 60*time.Second)
	defer cancel()
	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	client, closeFn, err := c.adapter(cctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer closeFn()
	list, err := scenario.List(cctx, client)
	if err != nil {
		return nil, toErr(err)
	}
	return map[string]any{"scenarios": list}, nil
}

func (rt *Runtime) scenarioDescribe(ctx context.Context, raw json.RawMessage, _ op.Sink) (any, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &a); err != nil || a.ID == "" {
		return nil, fail(op.CodeInvalidArgs, "id is required")
	}
	cctx, cancel := timeout(ctx, 60*time.Second)
	defer cancel()
	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	client, closeFn, err := c.adapter(cctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer closeFn()
	res, err := scenario.Describe(cctx, client, a.ID)
	if err != nil {
		return nil, toErr(err)
	}
	var desc any
	_ = json.Unmarshal(res, &desc)
	return map[string]any{"id": a.ID, "description": desc}, nil
}

func (rt *Runtime) scenarioRun(ctx context.Context, raw json.RawMessage, sink op.Sink) (any, error) {
	var a scenarioRunArgs
	if err := json.Unmarshal(raw, &a); err != nil || a.ID == "" {
		return nil, fail(op.CodeInvalidArgs, "id is required")
	}
	opts := scenario.RunOptions{ID: a.ID, Seed: a.Seed, WorldSeed: a.World, Params: a.Params}
	if a.Ticks > 0 {
		opts.Ticks = &a.Ticks
	}
	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	c.unmuted = a.Unmuted
	cctx, cancel := timeout(ctx, 3*time.Minute)
	defer cancel()

	var report *scenario.Report
	if a.Browser {
		srv, err := c.ensureServer(cctx, "dev", 0)
		if err != nil {
			return nil, toErr(err)
		}
		sess, cleanup, err := c.openBrowser(cctx, srv.URL())
		if err != nil {
			return nil, toErr(err)
		}
		defer cleanup()
		report, err = scenario.RunInBrowser(cctx, sess, opts)
		if err != nil {
			return nil, toErr(err)
		}
	} else {
		client, closeFn, err := c.adapter(cctx)
		if err != nil {
			return nil, toErr(err)
		}
		defer closeFn()
		report, err = scenario.Run(cctx, client, opts)
		if err != nil {
			return nil, toErr(err)
		}
	}
	out := scenarioRunOut{
		Scenario:  report.Scenario,
		Passed:    report.Passed(),
		Status:    report.Status,
		Digest:    report.Digest,
		SimSeed:   report.SimSeed,
		WorldSeed: report.WorldSeed,
		Ticks:     report.Ticks,
		Browser:   a.Browser,
		Checks:    []checkOut{},
	}
	for _, ch := range report.Checks {
		out.Checks = append(out.Checks, checkOut{Name: ch.Name, OK: ch.OK, Detail: ch.Detail})
	}
	if !out.Passed {
		return out, fail(op.CodeFailed, "one or more scenario checks failed")
	}
	return out, nil
}

type compareOut struct {
	Compared  int          `json:"compared"`
	Differing int          `json:"differing"`
	Results   []compareRow `json:"results"`
}

type compareRow struct {
	ID          string `json:"id"`
	Match       bool   `json:"match"`
	Headless    string `json:"headless"`
	Browser     string `json:"browser"`
	Digest      string `json:"digest"`
	Explanation string `json:"explanation,omitempty"`
}

func (rt *Runtime) scenarioCompare(ctx context.Context, raw json.RawMessage, sink op.Sink) (any, error) {
	var a struct {
		IDs     []string          `json:"ids"`
		All     bool              `json:"all"`
		Seed    string            `json:"seed"`
		World   string            `json:"world"`
		Ticks   int               `json:"ticks"`
		Params  map[string]string `json:"params"`
		Unmuted bool              `json:"unmuted"`
	}
	_ = json.Unmarshal(raw, &a)
	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	c.unmuted = a.Unmuted
	cctx, cancel := timeout(ctx, 10*time.Minute)
	defer cancel()

	client, closeFn, err := c.adapter(cctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer closeFn()

	ids := a.IDs
	if a.All || len(ids) == 0 {
		list, err := scenario.List(cctx, client)
		if err != nil {
			return nil, toErr(err)
		}
		ids = ids[:0]
		for _, s := range list {
			ids = append(ids, s.ID)
		}
	}
	srv, err := c.ensureServer(cctx, "dev", 0)
	if err != nil {
		return nil, toErr(err)
	}
	sess, cleanup, err := c.openBrowser(cctx, srv.URL())
	if err != nil {
		return nil, toErr(err)
	}
	defer cleanup()

	out := compareOut{Results: []compareRow{}}
	for _, id := range ids {
		opts := scenario.RunOptions{ID: id, Seed: a.Seed, WorldSeed: a.World, Params: a.Params}
		if a.Ticks > 0 {
			opts.Ticks = &a.Ticks
		}
		start := time.Now()
		res, err := scenario.Compare(cctx, client, sess, opts)
		if err != nil {
			out.Differing++
			sink.Stage("compare."+id, op.StatusFail, ms(start), "")
			continue
		}
		row := compareRow{ID: res.ID, Match: res.Match, Headless: res.Headless, Browser: res.Browser, Digest: res.Digest, Explanation: res.Explanation}
		out.Results = append(out.Results, row)
		if !res.Match {
			out.Differing++
		}
		sink.Stage("compare."+id, boolStatus(res.Match), ms(start), res.Explanation)
	}
	out.Compared = len(out.Results)
	if out.Differing > 0 {
		return out, fail(op.CodeFailed, "%d scenarios differ", out.Differing)
	}
	return out, nil
}

// ---- visual.* ---------------------------------------------------------------

type shotOut struct {
	Case   string `json:"case"`
	Ticks  int    `json:"ticks"`
	Region string `json:"region"`
	Output string `json:"output"`
	Bytes  int64  `json:"bytes"`
}

func (rt *Runtime) visualShot(ctx context.Context, raw json.RawMessage, sink op.Sink) (any, error) {
	var a struct {
		Case    string `json:"case"`
		Ticks   int    `json:"ticks"`
		Seed    string `json:"seed"`
		World   string `json:"world"`
		Out     string `json:"out"`
		Region  string `json:"region"`
		Expr    string `json:"expr"`
		Unmuted bool   `json:"unmuted"`
		LeaseMs int    `json:"leaseMs"`
	}
	_ = json.Unmarshal(raw, &a)
	if a.Region == "" {
		a.Region = "full"
	}
	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	c.unmuted = a.Unmuted
	m, err := c.manifest()
	if err != nil {
		return nil, toErr(err)
	}
	name := a.Case
	if name == "" {
		name = m.Visual.DefaultCase
	}
	if name == "" {
		return nil, fail(op.CodeInvalidArgs, "a visual case is required")
	}
	ticks := a.Ticks
	if ticks <= 0 {
		ticks = m.Visual.DefaultTicks
	}
	output := a.Out
	if output == "" {
		output = fmt.Sprintf("shot-%s-%d.png", name, ticks)
	}
	cctx, cancel := timeout(ctx, 3*time.Minute)
	defer cancel()
	sess, cleanup, err := c.browserSession(cctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer cleanup()
	res, err := visual.Shot(cctx, sess, visual.ShotOptions{
		Case: name, Ticks: ticks, Seed: a.Seed, WorldSeed: a.World, Output: output, Region: a.Region, Expr: a.Expr,
	})
	if err != nil {
		return nil, toErr(err)
	}
	sink.Artifact("image", "shot", res.Output)
	return shotOut{Case: res.Case, Ticks: res.Ticks, Region: a.Region, Output: res.Output, Bytes: res.Bytes}, nil
}

type sweepOut struct {
	Failed int                  `json:"failed"`
	Cases  []visual.SweepResult `json:"cases"`
}

func (rt *Runtime) visualSweep(ctx context.Context, raw json.RawMessage, sink op.Sink) (any, error) {
	var a struct {
		Ticks   int  `json:"ticks"`
		Unmuted bool `json:"unmuted"`
	}
	_ = json.Unmarshal(raw, &a)
	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	c.unmuted = a.Unmuted
	m, err := c.manifest()
	if err != nil {
		return nil, toErr(err)
	}
	ticks := a.Ticks
	if ticks <= 0 {
		ticks = m.Visual.DefaultTicks
	}
	cctx, cancel := timeout(ctx, 10*time.Minute)
	defer cancel()
	sess, cleanup, err := c.browserSession(cctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer cleanup()
	results, err := visual.Sweep(cctx, sess, ticks)
	if err != nil {
		return nil, toErr(err)
	}
	out := sweepOut{Cases: results}
	if out.Cases == nil {
		out.Cases = []visual.SweepResult{}
	}
	for _, r := range results {
		if !r.OK {
			out.Failed++
		}
		sink.Stage("case."+r.Case, boolStatus(r.OK), 0, r.Err)
	}
	if out.Failed > 0 {
		return out, fail(op.CodeFailed, "%d visual cases failed", out.Failed)
	}
	return out, nil
}

// browserSession returns the dev server and a browser session against it.
func (c *Core) browserSession(ctx context.Context) (*browser.Session, func(), error) {
	srv, err := c.ensureServer(ctx, "dev", 0)
	if err != nil {
		return nil, nil, err
	}
	return c.openBrowser(ctx, srv.URL())
}

// ---- visual.pixel -----------------------------------------------------------

type pixelSampleOut struct {
	X     int    `json:"x"`
	Y     int    `json:"y"`
	R     int    `json:"r"`
	G     int    `json:"g"`
	B     int    `json:"b"`
	A     int    `json:"a"`
	Color string `json:"color"`
}

type pixelOut struct {
	Image   string           `json:"image"`
	Width   int              `json:"width"`
	Height  int              `json:"height"`
	Samples []pixelSampleOut `json:"samples"`
}

func (rt *Runtime) visualPixel(ctx context.Context, raw json.RawMessage, sink op.Sink) (any, error) {
	var a struct {
		Points [][2]int `json:"points"`
		Case   string   `json:"case"`
		Ticks  int      `json:"ticks"`
		Seed   string   `json:"seed"`
		World  string   `json:"world"`
		Region string   `json:"region"`
		Image  string   `json:"image"`
		Out    string   `json:"out"`
	}
	if err := json.Unmarshal(raw, &a); err != nil || len(a.Points) == 0 {
		return nil, fail(op.CodeInvalidArgs, "at least one x,y point is required")
	}
	cctx, cancel := timeout(ctx, 5*time.Minute)
	defer cancel()

	path := a.Image
	cleanup := func() {}
	if path == "" {
		c, err := rt.core(ctx)
		if err != nil {
			return nil, toErr(err)
		}
		defer c.Close()
		m, err := c.manifest()
		if err != nil {
			return nil, toErr(err)
		}
		caseName := a.Case
		if caseName == "" {
			caseName = m.Visual.DefaultCase
		}
		ticks := a.Ticks
		if ticks <= 0 {
			ticks = m.Visual.DefaultTicks
		}
		dir := a.Out
		if dir == "" {
			tmp, err := os.MkdirTemp("", "game-forge-pixel-*")
			if err != nil {
				return nil, toErr(err)
			}
			cleanup = func() { _ = os.RemoveAll(tmp) }
			dir = filepath.Join(tmp, "sample.png")
		}
		sess, cleanup2, err := c.browserSession(cctx)
		if err != nil {
			cleanup()
			return nil, toErr(err)
		}
		defer cleanup2()
		if _, err := visual.Shot(cctx, sess, visual.ShotOptions{
			Case: caseName, Ticks: ticks, Seed: a.Seed, WorldSeed: a.World, Region: a.Region, Output: dir,
		}); err != nil {
			cleanup()
			return nil, toErr(err)
		}
		path = dir
	}
	defer cleanup()

	samples, w, h, err := sampleImage(path, a.Points)
	if err != nil {
		return nil, toErr(err)
	}
	sink.Artifact("image", "pixel-capture", path)
	return pixelOut{Image: path, Width: w, Height: h, Samples: samples}, nil
}

func sampleImage(path string, points [][2]int) ([]pixelSampleOut, int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode %s: %w", path, err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	samples := make([]pixelSampleOut, 0, len(points))
	for _, p := range points {
		if p[0] < 0 || p[1] < 0 || p[0] >= w || p[1] >= h {
			return nil, w, h, fmt.Errorf("coordinate (%d,%d) is outside the %dx%d image", p[0], p[1], w, h)
		}
		r, g, bl, a := img.At(b.Min.X+p[0], b.Min.Y+p[1]).RGBA()
		samples = append(samples, pixelSampleOut{
			X: p[0], Y: p[1],
			R: int(r >> 8), G: int(g >> 8), B: int(bl >> 8), A: int(a >> 8),
			Color: fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, bl>>8),
		})
	}
	return samples, w, h, nil
}

// ---- browser.* --------------------------------------------------------------

func (rt *Runtime) browserEval(ctx context.Context, raw json.RawMessage, _ op.Sink) (any, error) {
	var a struct {
		Expr    string `json:"expr"`
		Case    string `json:"case"`
		Ticks   int    `json:"ticks"`
		Seed    string `json:"seed"`
		World   string `json:"world"`
		Click   string `json:"click"`
		Press   string `json:"press"`
		Reload  bool   `json:"reload"`
		Unmuted bool   `json:"unmuted"`
		LeaseMs int    `json:"leaseMs"`
	}
	_ = json.Unmarshal(raw, &a)
	if a.Expr == "" {
		return nil, fail(op.CodeInvalidArgs, "expr is required")
	}
	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	c.unmuted = a.Unmuted
	if a.LeaseMs > 0 {
		c.lease = time.Duration(a.LeaseMs) * time.Millisecond
	}
	cctx, cancel := timeout(ctx, 5*time.Minute)
	defer cancel()
	sess, cleanup, err := c.browserSession(cctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer cleanup()
	_ = sess.P.ClearErrors(cctx)
	if a.Reload {
		if err := sess.P.Reload(cctx); err != nil {
			return nil, fail(op.CodeFailed, "reload: %v", err)
		}
	}
	if a.Click != "" {
		if err := sess.P.Click(cctx, a.Click); err != nil {
			return nil, fail(op.CodeFailed, "click %q: %v", a.Click, err)
		}
	}
	if a.Press != "" {
		if err := sess.P.Press(cctx, a.Press); err != nil {
			return nil, fail(op.CodeFailed, "press %q: %v", a.Press, err)
		}
	}
	if a.Case != "" {
		if err := loadVisualCase(cctx, sess, a.Case, a.Seed, a.World); err != nil {
			return nil, toErr(err)
		}
	}
	if a.Ticks > 0 {
		if _, err := sess.EvalString(cctx, fmt.Sprintf("window.__gameForge.advance(%d); 'ok'", a.Ticks)); err != nil {
			return nil, fail(op.CodeFailed, "advance: %v", err)
		}
	}
	res, err := sess.P.Eval(cctx, a.Expr)
	if err != nil {
		return nil, toErr(err)
	}
	var value any
	if err := json.Unmarshal(res, &value); err != nil {
		value = string(res)
	}
	return map[string]any{"value": value}, nil
}

type errorsOut struct {
	OK       bool     `json:"ok"`
	Messages []string `json:"messages"`
}

func (rt *Runtime) browserErrors(ctx context.Context, raw json.RawMessage, _ op.Sink) (any, error) {
	var a struct {
		Case    string `json:"case"`
		Ticks   int    `json:"ticks"`
		Unmuted bool   `json:"unmuted"`
	}
	_ = json.Unmarshal(raw, &a)
	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	c.unmuted = a.Unmuted
	cctx, cancel := timeout(ctx, 5*time.Minute)
	defer cancel()
	sess, cleanup, err := c.browserSession(cctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer cleanup()
	_ = sess.P.ClearErrors(cctx)
	if a.Case != "" {
		if err := loadVisualCase(cctx, sess, a.Case, "", ""); err != nil {
			return nil, toErr(err)
		}
	}
	if a.Ticks > 0 {
		if _, err := sess.EvalString(cctx, fmt.Sprintf("window.__gameForge.advance(%d); 'ok'", a.Ticks)); err != nil {
			return nil, fail(op.CodeFailed, "advance: %v", err)
		}
	}
	msgs := []string{}
	if errs, err := sess.P.PageErrors(cctx); err == nil {
		msgs = append(msgs, errs...)
	}
	if errs, err := sess.P.ConsoleErrors(cctx); err == nil {
		msgs = append(msgs, errs...)
	}
	msgs = dedupe(visual.FilterNoise(msgs))
	out := errorsOut{OK: len(msgs) == 0, Messages: msgs}
	if !out.OK {
		return out, fail(op.CodeFailed, "%d page/console diagnostics", len(msgs))
	}
	return out, nil
}

// ---- profile.run ------------------------------------------------------------

type profileStageOut struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	MS     int64  `json:"ms"`
	Detail string `json:"detail,omitempty"`
	Output string `json:"output,omitempty"`
}

type profileOut struct {
	Profile string            `json:"profile"`
	OK      bool              `json:"ok"`
	MS      int64             `json:"ms"`
	Stages  []profileStageOut `json:"stages"`
}

func (rt *Runtime) profileRun(ctx context.Context, raw json.RawMessage, sink op.Sink) (any, error) {
	var a struct {
		Profile string   `json:"profile"`
		Verbose bool     `json:"verbose"`
		Unmuted bool     `json:"unmuted"`
		Extra   []string `json:"extra"`
	}
	if err := json.Unmarshal(raw, &a); err != nil || a.Profile == "" {
		return nil, fail(op.CodeInvalidArgs, "profile is required")
	}
	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	c.unmuted = a.Unmuted
	m, err := c.manifest()
	if err != nil {
		return nil, toErr(err)
	}
	cctx, cancel := timeout(ctx, 15*time.Minute)
	defer cancel()

	sum, err := profile.Run(cctx, profile.Deps{
		Manifest:       m,
		Root:           m.Root,
		LogDir:         c.logDir,
		OpenBrowser:    c.openBrowser,
		OpenBrowserRaw: c.openBrowserRaw,
		EnsureServer:   c.ensureServer,
		Extra:          a.Extra,
		OnStage: func(res profile.StageResult) {
			sink.Stage(res.Name, boolStatus(res.OK), res.Duration.Milliseconds(), res.Detail)
		},
	}, a.Profile)
	if err != nil {
		return nil, toErr(err)
	}
	out := profileOut{Profile: sum.Profile, OK: sum.OK, MS: sum.Duration.Milliseconds(), Stages: []profileStageOut{}}
	for _, s := range sum.Stages {
		stage := profileStageOut{Name: s.Name, OK: s.OK, MS: s.Duration.Milliseconds(), Detail: s.Detail}
		if a.Verbose {
			stage.Output = s.Output
		}
		out.Stages = append(out.Stages, stage)
	}
	if !out.OK {
		return out, fail(op.CodeFailed, "profile %q failed", out.Profile)
	}
	return out, nil
}

// ---- server.* ---------------------------------------------------------------

type serverStartOut struct {
	Server  string `json:"server"`
	State   string `json:"state"`
	URL     string `json:"url"`
	PID     int    `json:"pid,omitempty"`
	Log     string `json:"log,omitempty"`
	LeaseMs int64  `json:"leaseMs,omitempty"`
}

func (rt *Runtime) serverStart(ctx context.Context, raw json.RawMessage, sink op.Sink) (any, error) {
	var a struct {
		Server  string `json:"server"`
		LeaseMs int    `json:"leaseMs"`
	}
	_ = json.Unmarshal(raw, &a)
	kind := a.Server
	if kind == "" {
		kind = "dev"
	}
	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	cctx, cancel := timeout(ctx, 3*time.Minute)
	defer cancel()
	lease := c.lease
	if a.LeaseMs > 0 {
		lease = time.Duration(a.LeaseMs) * time.Millisecond
	}
	srv, err := c.ensureServer(cctx, kind, lease)
	if err != nil {
		return nil, toErr(err)
	}
	state := "reused"
	if srv.Owned() {
		state = "started"
	}
	return serverStartOut{Server: kind, State: state, URL: srv.URL(), PID: srv.PID(), Log: srv.LogPath(), LeaseMs: lease.Milliseconds()}, nil
}

type serverStateOut struct {
	Server string `json:"server"`
	State  string `json:"state"`
	URL    string `json:"url"`
	Owned  bool   `json:"owned"`
	ID     string `json:"id,omitempty"`
	PID    int    `json:"pid,omitempty"`
}

func (rt *Runtime) serverStatus(ctx context.Context, raw json.RawMessage, _ op.Sink) (any, error) {
	return rt.serverReport(ctx, raw, false)
}

func (rt *Runtime) serverStop(ctx context.Context, raw json.RawMessage, _ op.Sink) (any, error) {
	return rt.serverReport(ctx, raw, true)
}

func (rt *Runtime) serverReport(ctx context.Context, raw json.RawMessage, stop bool) (any, error) {
	var a struct {
		Server string `json:"server"`
	}
	_ = json.Unmarshal(raw, &a)
	kind := a.Server
	if kind == "" {
		kind = "dev"
	}
	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	m, err := c.manifest()
	if err != nil {
		return nil, toErr(err)
	}
	spec, err := c.serverSpec(kind)
	if err != nil {
		return nil, toErr(err)
	}
	key := projectKeyFor(m)
	out := serverStateOut{Server: kind, URL: spec.URL}
	all, err := c.reg.List()
	if err != nil {
		return nil, toErr(err)
	}
	found := false
	foreign := ""
	for _, r := range all {
		if r.Kind != process.KindServer || r.Metadata["name"] != kind {
			continue
		}
		if !ownsResource(r, key, m.Project.ID) {
			// A same-named server owned by another project. Note it for the
			// report but never stop or lease it as ours.
			foreign = r.ProjectKey
			continue
		}
		found = true
		out.ID = r.ID
		out.PID = r.PID
		if stop {
			if _, _, _, err := c.stopNamedServer(kind); err != nil {
				return nil, toErr(err)
			}
			out.Owned = false
			out.State = "stopped"
			continue
		}
		out.Owned = true
		out.State = "owned"
	}
	if found {
		return out, nil
	}
	if foreign != "" {
		out.State = "running, owned by project " + foreign
	} else if out.URL != "" && server.Reachable(ctx, out.URL, 1500*time.Millisecond) {
		out.State = "reachable, not owned by game-forge"
	} else {
		out.State = "not running"
	}
	return out, nil
}

// ---- resource.* -------------------------------------------------------------

type resourceOut struct {
	ID         string `json:"id"`
	RunID      string `json:"runId,omitempty"`
	Project    string `json:"project,omitempty"`
	ProjectKey string `json:"projectKey,omitempty"`
	Kind       string `json:"kind"`
	Provider   string `json:"provider"`
	Namespace  string `json:"namespace,omitempty"`
	PID        int    `json:"pid,omitempty"`
	Host       string `json:"host,omitempty"`
	State      string `json:"state"`
	Expires    string `json:"expires,omitempty"`
}

func (rt *Runtime) resourceList(ctx context.Context, _ json.RawMessage, _ op.Sink) (any, error) {
	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	all, err := c.reg.List()
	if err != nil {
		return nil, toErr(err)
	}
	now := time.Now().UTC()
	out := []resourceOut{}
	for _, r := range all {
		st := "active"
		if r.Expired(now) {
			st = "expired"
		}
		e := resourceOut{
			ID: r.ID, RunID: r.RunID, Project: r.Project, ProjectKey: r.ProjectKey, Kind: r.Kind,
			Provider: r.Provider, Namespace: r.Namespace, PID: r.PID, Host: r.Host, State: st,
		}
		if !r.Expires.IsZero() {
			e.Expires = r.Expires.Format(time.RFC3339)
		}
		out = append(out, e)
	}
	return map[string]any{"resources": out}, nil
}

func (rt *Runtime) resourceGC(ctx context.Context, raw json.RawMessage, _ op.Sink) (any, error) {
	var a struct {
		TimeoutMs int `json:"timeoutMs"`
	}
	_ = json.Unmarshal(raw, &a)
	cctx, cancel := timeout(ctx, orDuration(a.TimeoutMs, 60*time.Second))
	defer cancel()
	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	res, err := c.tick(cctx)
	if err != nil {
		return nil, toErr(err)
	}
	return res, nil
}

func (rt *Runtime) resourceTick(ctx context.Context, _ json.RawMessage, _ op.Sink) (any, error) {
	cctx, cancel := timeout(ctx, 60*time.Second)
	defer cancel()
	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	res, err := c.tick(cctx)
	if err != nil {
		return nil, toErr(err)
	}
	return res, nil
}

// ---- gpu.run ----------------------------------------------------------------

func (rt *Runtime) gpuRun(ctx context.Context, raw json.RawMessage, sink op.Sink) (any, error) {
	var a struct {
		Scenario string `json:"scenario"`
		Ticks    int    `json:"ticks"`
		Frames   int    `json:"frames"`
		Unmuted  bool   `json:"unmuted"`
	}
	_ = json.Unmarshal(raw, &a)
	c, err := rt.core(ctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer c.Close()
	c.unmuted = a.Unmuted
	m, err := c.manifest()
	if err != nil {
		return nil, toErr(err)
	}
	name := a.Scenario
	if name == "" {
		name = m.GPU.Scenario
	}
	ticks := a.Ticks
	if ticks <= 0 {
		ticks = m.GPU.Ticks
	}
	frames := a.Frames
	if frames <= 0 {
		frames = m.GPU.Frames
	}
	cctx, cancel := timeout(ctx, 3*time.Minute)
	defer cancel()
	sess, cleanup, err := c.browserSession(cctx)
	if err != nil {
		return nil, toErr(err)
	}
	defer cleanup()
	res, err := gpu.Verify(cctx, sess, gpu.Options{Scenario: name, Ticks: ticks, Frames: frames})
	if res == nil {
		if err != nil {
			return nil, toErr(err)
		}
		return nil, fail(op.CodeFailed, "gpu verification returned no result")
	}
	if !res.OK {
		return res, fail(op.CodeFailed, "gpu verification failed")
	}
	return res, nil
}

// ---- helpers ----------------------------------------------------------------

// loadVisualCase arranges a visual case through the bridge.
func loadVisualCase(ctx context.Context, sess *browser.Session, name, seed, world string) error {
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

// dedupe removes duplicate messages, preserving order.
func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, m := range in {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

func boolStatus(ok bool) string {
	if ok {
		return op.StatusPass
	}
	return op.StatusFail
}

func ms(start time.Time) int64 { return time.Since(start).Milliseconds() }

func orDuration(ms int, def time.Duration) time.Duration {
	if ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return def
}
