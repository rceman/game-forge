package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rceman/game-forge/internal/browser"
	"github.com/rceman/game-forge/internal/config"
	"github.com/rceman/game-forge/internal/gpu"
	"github.com/rceman/game-forge/internal/profile"
	"github.com/rceman/game-forge/internal/scenario"
	"github.com/rceman/game-forge/internal/scheduler"
	"github.com/rceman/game-forge/internal/server"
	"github.com/rceman/game-forge/internal/visual"
)

// ---- scenario --------------------------------------------------------------

func cmdScenario(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "game-forge scenario: expected subcommand (list|describe|run|compare)")
		return ExitUsage
	}
	switch args[0] {
	case "list":
		return cmdScenarioList(args[1:])
	case "describe":
		return cmdScenarioDescribe(args[1:])
	case "run":
		return cmdScenarioRun(args[1:])
	case "compare":
		return cmdScenarioCompare(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "game-forge scenario: unknown subcommand %q\n", args[0])
		return ExitUsage
	}
}

func cmdScenarioList(args []string) int {
	fs := newFlagSet("scenario list")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if code, done := fs.parse(args); done {
		return code
	}
	h, err := newHarness()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario list: %v\n", err)
		return ExitFail
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, closeFn, err := h.adapter(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario list: %v\n", err)
		return ExitFail
	}
	defer closeFn()

	list, err := scenario.List(ctx, client)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario list: %v\n", err)
		return ExitFail
	}
	if *jsonOut {
		return emitJSON(list)
	}
	for _, s := range list {
		fmt.Printf("%-22s %s\n", s.ID, s.Summary)
		for _, p := range s.Params {
			bounds := ""
			if p.Min != nil || p.Max != nil {
				bounds = fmt.Sprintf(" [%v..%v]", deref(p.Min), deref(p.Max))
			}
			fmt.Printf("  --%s=%s%s  %s\n", p.Name, p.Default, bounds, p.Help)
		}
	}
	return ExitOK
}

func cmdScenarioDescribe(args []string) int {
	fs := newFlagSet("scenario describe")
	if code, done := fs.parse(args); done {
		return code
	}
	id := first(fs.Args())
	if id == "" {
		fmt.Fprintln(os.Stderr, "game-forge scenario describe: expected a scenario id")
		return ExitUsage
	}
	h, err := newHarness()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario describe: %v\n", err)
		return ExitFail
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, closeFn, err := h.adapter(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario describe: %v\n", err)
		return ExitFail
	}
	defer closeFn()

	raw, err := scenario.Describe(ctx, client, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario describe: %v\n", err)
		return ExitFail
	}
	return emitJSONRaw(raw)
}

func cmdScenarioRun(args []string) int {
	fs := newFlagSet("scenario run")
	seed := fs.String("seed", "", "simulation seed (preset name or integer)")
	world := fs.String("world", "", "world seed (preset name or integer)")
	ticks := fs.Int("ticks", -1, "advance this many ticks (default: scenario default)")
	browserMode := fs.Bool("browser", false, "run in the native browser instead of headless")
	jsonOut := fs.Bool("json", false, "emit JSON")
	unmuted := fs.Bool("unmuted", false, "do not suppress browser audio output (diagnostic override)")
	var params StringList
	fs.Var(&params, "param", "scenario parameter key=value (repeatable)")
	if code, done := fs.parse(args); done {
		return code
	}
	id := first(fs.Args())
	if id == "" {
		fmt.Fprintln(os.Stderr, "game-forge scenario run: expected a scenario id")
		return ExitUsage
	}
	opts, err := runOptions(id, *seed, *world, *ticks, params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario run: %v\n", err)
		return ExitUsage
	}

	h, err := newHarness()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario run: %v\n", err)
		return ExitFail
	}
	h.unmuted = *unmuted
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var report *scenario.Report
	if *browserMode {
		srv, err := h.ensureServer(ctx, "dev", 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "game-forge scenario run: %v\n", err)
			return ExitFail
		}
		defer h.stopOwnedServers()
		sess, cleanup, err := h.openBrowser(ctx, srv.URL())
		if err != nil {
			fmt.Fprintf(os.Stderr, "game-forge scenario run: %v\n", err)
			return ExitFail
		}
		defer cleanup()
		report, err = scenario.RunInBrowser(ctx, sess, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "game-forge scenario run (browser): %v\n", err)
			return ExitFail
		}
	} else {
		client, closeFn, err := h.adapter(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "game-forge scenario run: %v\n", err)
			return ExitFail
		}
		defer closeFn()
		report, err = scenario.Run(ctx, client, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "game-forge scenario run: %v\n", err)
			return ExitFail
		}
	}

	if *jsonOut {
		if code := emitJSON(report); code != ExitOK {
			return code
		}
	} else {
		fmt.Println(scenario.FormatReport(report, true))
	}
	if !report.Passed() {
		return ExitFail
	}
	return ExitOK
}

func cmdScenarioCompare(args []string) int {
	fs := newFlagSet("scenario compare")
	seed := fs.String("seed", "", "simulation seed")
	world := fs.String("world", "", "world seed")
	ticks := fs.Int("ticks", -1, "advance this many ticks")
	all := fs.Bool("all", false, "compare every registered scenario")
	jsonOut := fs.Bool("json", false, "emit JSON")
	unmuted := fs.Bool("unmuted", false, "do not suppress browser audio output (diagnostic override)")
	var params StringList
	fs.Var(&params, "param", "scenario parameter key=value (repeatable)")
	if code, done := fs.parse(args); done {
		return code
	}

	h, err := newHarness()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario compare: %v\n", err)
		return ExitFail
	}
	h.unmuted = *unmuted
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	client, closeFn, err := h.adapter(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario compare: %v\n", err)
		return ExitFail
	}
	defer closeFn()

	ids := fs.Args()
	if *all || len(ids) == 0 {
		list, err := scenario.List(ctx, client)
		if err != nil {
			fmt.Fprintf(os.Stderr, "game-forge scenario compare: %v\n", err)
			return ExitFail
		}
		ids = ids[:0]
		for _, s := range list {
			ids = append(ids, s.ID)
		}
	}

	srv, err := h.ensureServer(ctx, "dev", 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario compare: %v\n", err)
		return ExitFail
	}
	defer h.stopOwnedServers()
	sess, cleanup, err := h.openBrowser(ctx, srv.URL())
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario compare: %v\n", err)
		return ExitFail
	}
	defer cleanup()

	failed := 0
	results := make([]*scenario.CompareResult, 0, len(ids))
	for _, id := range ids {
		opts, err := runOptions(id, *seed, *world, *ticks, params)
		if err != nil {
			fmt.Fprintf(os.Stderr, "game-forge scenario compare: %v\n", err)
			return ExitUsage
		}
		res, err := scenario.Compare(ctx, client, sess, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL compare %s: %v\n", id, err)
			failed++
			continue
		}
		results = append(results, res)
		if !res.Match {
			failed++
		}
	}
	if *jsonOut {
		if code := emitJSON(results); code != ExitOK {
			return code
		}
	} else {
		for _, r := range results {
			mark := "MATCH"
			if !r.Match {
				mark = "DIFF"
			}
			fmt.Printf("%-6s %-22s headless=%s browser=%s digest=%s\n", mark, r.ID, r.Headless, r.Browser, r.Digest)
			if !r.Match {
				fmt.Printf("       %s\n", r.Explanation)
			}
		}
		fmt.Printf("compared %d scenarios, %d differing\n", len(results), failed)
	}
	if failed > 0 {
		return ExitFail
	}
	return ExitOK
}

// ---- eval ------------------------------------------------------------------

// cmdEval is the generic browser inspection command: it loads a declared visual
// case, advances deterministically, performs optional trusted input, then
// evaluates a project-supplied expression. Game Forge owns the browser; the
// project owns the meaning of the expression.
func cmdEval(args []string) int {
	fs := newFlagSet("eval")
	expr := fs.String("expr", "", "JavaScript expression to evaluate")
	caseName := fs.String("case", "", "load this visual case first")
	ticks := fs.Int("ticks", -1, "advance this many ticks")
	seed := fs.String("seed", "", "simulation seed")
	world := fs.String("world", "", "world seed")
	click := fs.String("click", "", "click this selector first (a trusted input event)")
	press := fs.String("press", "", "press this key first")
	reload := fs.Bool("reload", false, "reload the page before evaluating")
	showErrors := fs.Bool("errors", false, "print fresh page/console diagnostics instead")
	raw := fs.Bool("raw", false, "print the raw JSON result")
	unmuted := fs.Bool("unmuted", false, "do not suppress browser audio output (diagnostic override)")
	lease := fs.Duration("lease", 10*time.Minute, "owned-resource lease")
	if code, done := fs.parse(args); done {
		return code
	}
	if *expr == "" && !*showErrors {
		fmt.Fprintln(os.Stderr, "game-forge eval: --expr is required")
		return ExitUsage
	}

	h, err := newHarness()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge eval: %v\n", err)
		return ExitFail
	}
	h.lease = *lease
	h.unmuted = *unmuted
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	srv, err := h.ensureServer(ctx, "dev", 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge eval: %v\n", err)
		return ExitFail
	}
	defer h.stopOwnedServers()
	sess, cleanup, err := h.openBrowser(ctx, srv.URL())
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge eval: %v\n", err)
		return ExitFail
	}
	defer cleanup()

	_ = sess.P.ClearErrors(ctx)
	if *reload {
		if err := sess.P.Reload(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "game-forge eval: reload: %v\n", err)
			return ExitFail
		}
	}
	if *click != "" {
		if err := sess.P.Click(ctx, *click); err != nil {
			fmt.Fprintf(os.Stderr, "game-forge eval: click %q: %v\n", *click, err)
			return ExitFail
		}
	}
	if *press != "" {
		if err := sess.P.Press(ctx, *press); err != nil {
			fmt.Fprintf(os.Stderr, "game-forge eval: press %q: %v\n", *press, err)
			return ExitFail
		}
	}
	if *caseName != "" {
		if err := loadVisualCase(ctx, sess, *caseName, *seed, *world); err != nil {
			fmt.Fprintf(os.Stderr, "game-forge eval: %v\n", err)
			return ExitFail
		}
	}
	if *ticks > 0 {
		if _, err := sess.EvalString(ctx, fmt.Sprintf("window.__gameForge.advance(%d); 'ok'", *ticks)); err != nil {
			fmt.Fprintf(os.Stderr, "game-forge eval: advance: %v\n", err)
			return ExitFail
		}
	}

	if *showErrors {
		return printDiagnostics(ctx, sess)
	}
	res, err := sess.P.Eval(ctx, *expr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge eval: %v\n", err)
		return ExitFail
	}
	if *raw {
		fmt.Println(string(res))
		return ExitOK
	}
	var s string
	if err := json.Unmarshal(res, &s); err == nil {
		fmt.Println(s)
		return ExitOK
	}
	var v any
	if err := json.Unmarshal(res, &v); err == nil {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
		return ExitOK
	}
	fmt.Println(string(res))
	return ExitOK
}

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

// printDiagnostics prints fresh, deduplicated page and console diagnostics.
func printDiagnostics(ctx context.Context, sess *browser.Session) int {
	msgs := []string{}
	if errs, err := sess.P.PageErrors(ctx); err == nil {
		msgs = append(msgs, errs...)
	}
	if errs, err := sess.P.ConsoleErrors(ctx); err == nil {
		msgs = append(msgs, errs...)
	}
	msgs = dedupe(visual.FilterNoise(msgs))
	if len(msgs) == 0 {
		fmt.Println("PASS no page/console diagnostics")
		return ExitOK
	}
	for _, m := range msgs {
		fmt.Println(m)
	}
	return ExitFail
}

// dedupe removes duplicate messages, preserving order.
func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, m := range in {
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// ---- shot / sweep ----------------------------------------------------------

func cmdShot(args []string) int {
	fs := newFlagSet("shot")
	ticks := fs.Int("ticks", -1, "advance this many ticks")
	seed := fs.String("seed", "", "simulation seed")
	world := fs.String("world", "", "world seed")
	out := fs.String("out", "", "output PNG path")
	region := fs.String("region", "full", "named region (or full)")
	expr := fs.String("expr", "", "project JavaScript to run after load/advance, before capture")
	unmuted := fs.Bool("unmuted", false, "do not suppress browser audio output (diagnostic override)")
	lease := fs.Duration("lease", 10*time.Minute, "how long owned resources stay reclaimable after an abnormal exit")
	if code, done := fs.parse(args); done {
		return code
	}

	h, err := newHarness()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge shot: %v\n", err)
		return ExitFail
	}
	h.lease = *lease
	h.unmuted = *unmuted
	name := first(fs.Args())
	if name == "" {
		name = h.m.Visual.DefaultCase
	}
	if name == "" {
		fmt.Fprintln(os.Stderr, "game-forge shot: expected a visual case name")
		return ExitUsage
	}
	if *ticks < 0 {
		*ticks = h.m.Visual.DefaultTicks
	}
	output := *out
	if output == "" {
		output = fmt.Sprintf("shot-%s-%d.png", name, *ticks)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	srv, err := h.ensureServer(ctx, "dev", 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge shot: %v\n", err)
		return ExitFail
	}
	defer h.stopOwnedServers()
	sess, cleanup, err := h.openBrowser(ctx, srv.URL())
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge shot: %v\n", err)
		return ExitFail
	}
	defer cleanup()

	res, err := visual.Shot(ctx, sess, visual.ShotOptions{
		Case: name, Ticks: *ticks, Seed: *seed, WorldSeed: *world, Output: output, Region: *region, Expr: *expr,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge shot: %v\n", err)
		return ExitFail
	}
	fmt.Printf("case=%s ticks=%d region=%s\nscreenshot=%s (%d bytes)\n", res.Case, res.Ticks, *region, res.Output, res.Bytes)
	return ExitOK
}

func cmdSweep(args []string) int {
	fs := newFlagSet("sweep")
	ticks := fs.Int("ticks", -1, "advance this many ticks per case")
	jsonOut := fs.Bool("json", false, "emit JSON")
	unmuted := fs.Bool("unmuted", false, "do not suppress browser audio output (diagnostic override)")
	if code, done := fs.parse(args); done {
		return code
	}
	h, err := newHarness()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge sweep: %v\n", err)
		return ExitFail
	}
	h.unmuted = *unmuted
	if *ticks < 0 {
		*ticks = h.m.Visual.DefaultTicks
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	srv, err := h.ensureServer(ctx, "dev", 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge sweep: %v\n", err)
		return ExitFail
	}
	defer h.stopOwnedServers()
	sess, cleanup, err := h.openBrowser(ctx, srv.URL())
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge sweep: %v\n", err)
		return ExitFail
	}
	defer cleanup()

	results, err := visual.Sweep(ctx, sess, *ticks)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge sweep: %v\n", err)
		return ExitFail
	}
	if *jsonOut {
		if code := emitJSON(results); code != ExitOK {
			return code
		}
	} else {
		failed := 0
		for _, r := range results {
			mark := "ok"
			if !r.OK {
				mark = "FAIL"
				failed++
			}
			line := fmt.Sprintf("%-26s %-8s %s", r.Case, r.Kind, mark)
			if r.Err != "" {
				line += ": " + r.Err
			}
			fmt.Println(line)
		}
		fmt.Printf("swept %d cases, %d failed\n", len(results), failed)
	}
	for _, r := range results {
		if !r.OK {
			return ExitFail
		}
	}
	return ExitOK
}

// ---- gpu -------------------------------------------------------------------

func cmdGPU(args []string) int {
	fs := newFlagSet("gpu")
	scenarioName := fs.String("scenario", "", "benchmark scenario/state (default: manifest)")
	ticks := fs.Int("ticks", -1, "advance this many ticks before benchmarking")
	frames := fs.Int("frames", -1, "benchmark frames")
	jsonOut := fs.Bool("json", false, "emit JSON")
	unmuted := fs.Bool("unmuted", false, "do not suppress browser audio output (diagnostic override)")
	if code, done := fs.parse(args); done {
		return code
	}
	h, err := newHarness()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge gpu: %v\n", err)
		return ExitFail
	}
	h.unmuted = *unmuted
	name := *scenarioName
	if name == "" {
		name = h.m.GPU.Scenario
	}
	if *ticks < 0 {
		*ticks = h.m.GPU.Ticks
	}
	if *frames < 0 {
		*frames = h.m.GPU.Frames
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	srv, err := h.ensureServer(ctx, "dev", 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge gpu: %v\n", err)
		return ExitFail
	}
	defer h.stopOwnedServers()
	sess, cleanup, err := h.openBrowser(ctx, srv.URL())
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge gpu: %v\n", err)
		return ExitFail
	}
	defer cleanup()

	res, err := gpu.Verify(ctx, sess, gpu.Options{Scenario: name, Ticks: *ticks, Frames: *frames})
	if res == nil {
		if err != nil {
			fmt.Fprintf(os.Stderr, "game-forge gpu: %v\n", err)
		}
		return ExitFail
	}
	if *jsonOut {
		if code := emitJSON(res); code != ExitOK {
			return code
		}
	} else {
		fmt.Println("game-forge gpu")
		fmt.Printf("  vendor:            %s\n", res.Renderer.Vendor)
		fmt.Printf("  unmasked vendor:   %s\n", res.Renderer.UnmaskedVendor)
		fmt.Printf("  renderer:          %s\n", res.Renderer.Renderer)
		fmt.Printf("  unmasked renderer: %s\n", res.Renderer.UnmaskedRenderer)
		fmt.Printf("  software:          %v\n", res.Software)
		if res.Benchmark != nil {
			b := res.Benchmark
			fmt.Printf("  benchmark:         scenario=%s frames=%d meanFrameMs=%.2f draws=%d tris=%d textures=%d enemies=%d projectiles=%d\n",
				res.Scenario, b.Frames, b.MeanFrameMs, b.DrawCalls, b.Triangles, b.Textures, b.Enemies, b.Projectiles)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge gpu: %v\n", err)
		return ExitFail
	}
	return ExitOK
}

// ---- test / verify ---------------------------------------------------------

func cmdProfile(profileName string, args []string) int {
	fs := newFlagSet(profileName)
	jsonOut := fs.Bool("json", false, "emit JSON")
	verbose := fs.Bool("verbose", false, "include stage output")
	unmuted := fs.Bool("unmuted", false, "do not suppress browser audio output (diagnostic override)")
	if code, done := fs.parse(args); done {
		return code
	}
	// "verify full" is accepted as an alias for the verify_full profile.
	if profileName == "verify" && len(fs.Args()) > 0 && fs.Args()[0] == "full" {
		profileName = "verify_full"
	}

	h, err := newHarness()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge %s: %v\n", profileName, err)
		return ExitFail
	}
	if _, ok := h.m.Profile(profileName); !ok {
		fmt.Fprintf(os.Stderr, "game-forge %s: project declares no profile %q\n", profileName, profileName)
		return ExitUsage
	}
	// Trailing arguments are forwarded to a single-command profile, so a
	// native tool's own filters still flow through Game Forge's execution,
	// timeout and exit-status handling instead of a project-local wrapper.
	extra := fs.Args()
	if profileName == "verify_full" && len(extra) > 0 && extra[0] == "full" {
		extra = extra[1:]
	}
	if len(extra) > 0 {
		stages, _ := h.m.Profile(profileName)
		if len(stages) != 1 || stages[0].Uses != "" || len(stages[0].Command) == 0 {
			fmt.Fprintf(os.Stderr, "game-forge %s: profile %q is not a single command, so it accepts no extra arguments\n", profileName, profileName)
			return ExitUsage
		}
	}
	h.unmuted = *unmuted
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	deps := profile.Deps{
		Manifest: h.m,
		Root:     h.m.Root,
		LogDir:   h.logDir,
		OpenBrowser: func(ctx context.Context, url string) (*browser.Session, func(), error) {
			return h.openBrowser(ctx, url)
		},
		OpenBrowserRaw: func(ctx context.Context, url string) (*browser.Session, func(), error) {
			return h.openBrowserRaw(ctx, url)
		},
		EnsureServer: func(ctx context.Context, kind string, lease time.Duration) (*server.Server, error) {
			return h.ensureServer(ctx, kind, lease)
		},
		Extra: extra,
	}
	sum, err := profile.Run(ctx, deps, profileName)
	h.stopOwnedServers()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge %s: %v\n", profileName, err)
		return ExitFail
	}
	if *jsonOut {
		if code := emitJSON(sum); code != ExitOK {
			return code
		}
	} else {
		fmt.Println(sum.Format(*verbose))
	}
	if !sum.OK {
		return ExitFail
	}
	return ExitOK
}

// ---- tick / scheduler ------------------------------------------------------

func cmdTick(args []string) int {
	fs := newFlagSet("tick")
	timeout := fs.Duration("timeout", 60*time.Second, "overall timeout")
	if code, done := fs.parse(args); done {
		return code
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	return runReclaim(ctx, true)
}

func cmdScheduler(args []string) int {
	sub := first(args)
	rest := args
	if sub != "" {
		rest = args[1:]
	}
	fs := newFlagSet("scheduler")
	if code, done := fs.parse(rest); done {
		return code
	}
	be, err := scheduler.Detect()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scheduler: %v\n", err)
		return ExitFail
	}
	stateDir, err := config.StateDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scheduler: %v\n", err)
		return ExitFail
	}
	logPath := stateDir + "/scheduler.log"

	switch sub {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "game-forge scheduler install: %v\n", err)
			return ExitFail
		}
		if err := be.Install(exe, logPath); err != nil {
			fmt.Fprintf(os.Stderr, "game-forge scheduler install: %v\n", err)
			return ExitFail
		}
		fmt.Printf("scheduler installed (%s): runs \"%s tick\" every minute\n", be.Name(), exe)
		return ExitOK
	case "status":
		ok, entry, err := be.Status()
		if err != nil {
			fmt.Fprintf(os.Stderr, "game-forge scheduler status: %v\n", err)
			return ExitFail
		}
		if ok {
			fmt.Printf("scheduler: installed (%s)\n  %s\n", be.Name(), entry)
		} else {
			fmt.Printf("scheduler: not installed (%s)\n", be.Name())
		}
		return ExitOK
	case "uninstall":
		if err := be.Uninstall(); err != nil {
			fmt.Fprintf(os.Stderr, "game-forge scheduler uninstall: %v\n", err)
			return ExitFail
		}
		fmt.Printf("scheduler uninstalled (%s)\n", be.Name())
		return ExitOK
	default:
		fmt.Fprintln(os.Stderr, "game-forge scheduler: expected subcommand (install|status|uninstall)")
		return ExitUsage
	}
}

// ---- helpers ---------------------------------------------------------------

func runOptions(id, seed, world string, ticks int, params StringList) (scenario.RunOptions, error) {
	opts := scenario.RunOptions{ID: id, Seed: seed, WorldSeed: world}
	if ticks >= 0 {
		t := ticks
		opts.Ticks = &t
	}
	if len(params) > 0 {
		m := map[string]string{}
		for _, p := range params {
			k, v, ok := strings.Cut(p, "=")
			if !ok || k == "" {
				return opts, fmt.Errorf("--param expects key=value, got %q", p)
			}
			m[k] = v
		}
		opts.Params = m
	}
	return opts, nil
}

func emitJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "encode JSON: %v\n", err)
		return ExitFail
	}
	return ExitOK
}

func emitJSONRaw(raw json.RawMessage) int {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		fmt.Println(string(raw))
		return ExitOK
	}
	return emitJSON(v)
}

func first(a []string) string {
	if len(a) == 0 {
		return ""
	}
	return a[0]
}

func deref(p *int) any {
	if p == nil {
		return "*"
	}
	return *p
}
