package cli

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// call is one CLI command translated into an operation request plus a human
// renderer for its result. The CLI never implements the operation itself.
type call struct {
	op     string
	args   map[string]any
	stream bool
	render renderFunc
	om     *outputMode
}

// dispatch maps CLI syntax to an operation. It returns (call, exitCode, done);
// done is true when the caller should return exitCode (help, usage error, or a
// locally-handled command).
func dispatch(args []string) (*call, int, bool) {
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "doctor":
		return cmdDoctor(rest)
	case "ps":
		return cmdPs(rest)
	case "gc":
		return cmdGC(rest)
	case "tick":
		return cmdTick(rest)
	case "serve":
		return cmdServe(rest)
	case "project":
		return cmdProject(rest)
	case "scenario":
		return cmdScenario(rest)
	case "shot":
		return cmdShot(rest)
	case "pixel":
		return cmdPixel(rest)
	case "eval":
		return cmdEval(rest)
	case "errors":
		return cmdErrors(rest)
	case "sweep":
		return cmdSweep(rest)
	case "gpu":
		return cmdGPU(rest)
	case "test", "build", "prod", "check", "selfcheck", "policy":
		return cmdProfileNamed(cmd, rest)
	case "verify":
		return cmdVerify(rest)
	case "profile":
		name := ""
		if len(rest) > 0 {
			name = rest[0]
			rest = rest[1:]
		}
		if name == "" {
			fmt.Fprintln(os.Stderr, "game-forge profile: expected a profile name")
			return nil, ExitUsage, true
		}
		return cmdProfileNamed(name, rest)
	default:
		// Any declared profile name is runnable directly; the daemon reports an
		// unknown profile if the name is not one.
		return cmdProfileNamed(cmd, rest)
	}
}

// cmdFlags returns a per-command flagSet. Structured-output flags are added by
// outputMode.register so each is defined exactly once.
func cmdFlags(name string) *flagSet {
	return newFlagSet(name)
}

// outputMode reads the structured-output flags after parsing.
type outputMode struct {
	json, ndjson bool
}

func (o *outputMode) register(f *flagSet) {
	f.fs.BoolVar(&o.json, "json", false, "emit the result as JSON")
	f.fs.BoolVar(&o.ndjson, "ndjson", false, "emit the raw NDJSON event stream")
	f.bools["json"] = true
	f.bools["ndjson"] = true
}

// cmdDoctor → doctor.run
func cmdDoctor(args []string) (*call, int, bool) {
	fs := cmdFlags("doctor")
	var om outputMode
	om.register(fs)
	leave := fs.Bool("no-cleanup", false, "leave the resource record for gc")
	lease := fs.Duration("lease", 5*time.Minute, "resource lease")
	timeout := fs.Duration("timeout", 90*time.Second, "overall timeout")
	unmuted := fs.Bool("unmuted", false, "audible browser output (diagnostic)")
	if code, done := fs.parse(args); done {
		return nil, code, true
	}
	a := map[string]any{}
	if *leave {
		a["noCleanup"] = true
	}
	if *unmuted {
		a["unmuted"] = true
	}
	a["leaseMs"] = int(lease.Milliseconds())
	a["timeoutMs"] = int(timeout.Milliseconds())
	return &call{om: &om, op: "doctor.run", args: a, stream: true, render: renderDoctor}, 0, false
}

// cmdPs → resource.list
func cmdPs(args []string) (*call, int, bool) {
	fs := cmdFlags("ps")
	var om outputMode
	om.register(fs)
	if code, done := fs.parse(args); done {
		return nil, code, true
	}
	return &call{om: &om, op: "resource.list", args: map[string]any{}, render: renderResourceList}, 0, false
}

// cmdGC → resource.gc
func cmdGC(args []string) (*call, int, bool) {
	fs := cmdFlags("gc")
	var om outputMode
	om.register(fs)
	timeout := fs.Duration("timeout", 60*time.Second, "overall timeout")
	if code, done := fs.parse(args); done {
		return nil, code, true
	}
	a := map[string]any{"timeoutMs": int(timeout.Milliseconds())}
	return &call{om: &om, op: "resource.gc", args: a, stream: true, render: renderReclaim}, 0, false
}

// cmdTick → resource.tick
func cmdTick(args []string) (*call, int, bool) {
	fs := cmdFlags("tick")
	var om outputMode
	om.register(fs)
	if code, done := fs.parse(args); done {
		return nil, code, true
	}
	return &call{om: &om, op: "resource.tick", args: map[string]any{}, render: renderReclaim}, 0, false
}

// cmdServe → server.start|status|stop
func cmdServe(args []string) (*call, int, bool) {
	fs := cmdFlags("serve")
	var om outputMode
	om.register(fs)
	kind := fs.String("server", "dev", "server to manage (dev|prod)")
	lease := fs.Duration("lease", 30*time.Minute, "abandoned-server lease")
	if code, done := fs.parse(args); done {
		return nil, code, true
	}
	action := "start"
	if rest := fs.Args(); len(rest) > 0 {
		action = rest[0]
	}
	if action != "start" && action != "status" && action != "stop" {
		fmt.Fprintf(os.Stderr, "game-forge serve: unknown action %q (start|status|stop)\n", action)
		return nil, ExitUsage, true
	}
	a := map[string]any{"server": *kind}
	if action == "start" {
		a["leaseMs"] = int(lease.Milliseconds())
		return &call{om: &om, op: "server.start", args: a, stream: true, render: renderServerStart}, 0, false
	}
	if action == "stop" {
		return &call{om: &om, op: "server.stop", args: a, stream: true, render: renderServerState}, 0, false
	}
	return &call{om: &om, op: "server.status", args: a, render: renderServerState}, 0, false
}

// cmdProject → project.info
func cmdProject(args []string) (*call, int, bool) {
	if len(args) == 0 || args[0] != "info" {
		fmt.Fprintln(os.Stderr, "game-forge project: expected subcommand (info)")
		return nil, ExitUsage, true
	}
	fs := cmdFlags("project info")
	var om outputMode
	om.register(fs)
	if code, done := fs.parse(args[1:]); done {
		return nil, code, true
	}
	return &call{om: &om, op: "project.info", args: map[string]any{}, render: renderProjectInfo}, 0, false
}

// cmdScenario → scenario.*
func cmdScenario(args []string) (*call, int, bool) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "game-forge scenario: expected subcommand (list|describe|run|compare)")
		return nil, ExitUsage, true
	}
	switch args[0] {
	case "list":
		fs := cmdFlags("scenario list")
		var om outputMode
		om.register(fs)
		if code, done := fs.parse(args[1:]); done {
			return nil, code, true
		}
		return &call{om: &om, op: "scenario.list", args: map[string]any{}, render: renderScenarioList}, 0, false
	case "describe":
		fs := cmdFlags("scenario describe")
		var om outputMode
		om.register(fs)
		if code, done := fs.parse(args[1:]); done {
			return nil, code, true
		}
		id := first(fs.Args())
		if id == "" {
			fmt.Fprintln(os.Stderr, "game-forge scenario describe: expected a scenario id")
			return nil, ExitUsage, true
		}
		return &call{om: &om, op: "scenario.describe", args: map[string]any{"id": id}, render: renderJSON}, 0, false
	case "run":
		return cmdScenarioRun(args[1:])
	case "compare":
		return cmdScenarioCompare(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "game-forge scenario: unknown subcommand %q\n", args[0])
		return nil, ExitUsage, true
	}
}

func cmdScenarioRun(args []string) (*call, int, bool) {
	fs := cmdFlags("scenario run")
	var om outputMode
	om.register(fs)
	seed := fs.String("seed", "", "simulation seed")
	world := fs.String("world", "", "world seed")
	ticks := fs.Int("ticks", -1, "advance this many ticks")
	browserMode := fs.Bool("browser", false, "run in the native browser")
	unmuted := fs.Bool("unmuted", false, "audible browser output (diagnostic)")
	var params StringList
	fs.Var(&params, "param", "scenario parameter key=value (repeatable)")
	if code, done := fs.parse(args); done {
		return nil, code, true
	}
	id := first(fs.Args())
	if id == "" {
		fmt.Fprintln(os.Stderr, "game-forge scenario run: expected a scenario id")
		return nil, ExitUsage, true
	}
	a := map[string]any{"id": id}
	if *seed != "" {
		a["seed"] = *seed
	}
	if *world != "" {
		a["world"] = *world
	}
	if *ticks >= 0 {
		a["ticks"] = *ticks
	}
	if *browserMode {
		a["browser"] = true
	}
	if *unmuted {
		a["unmuted"] = true
	}
	if pm, err := paramMap(params); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario run: %v\n", err)
		return nil, ExitUsage, true
	} else if len(pm) > 0 {
		a["params"] = pm
	}
	return &call{om: &om, op: "scenario.run", args: a, stream: true, render: renderScenarioRun}, 0, false
}

func cmdScenarioCompare(args []string) (*call, int, bool) {
	fs := cmdFlags("scenario compare")
	var om outputMode
	om.register(fs)
	seed := fs.String("seed", "", "simulation seed")
	world := fs.String("world", "", "world seed")
	ticks := fs.Int("ticks", -1, "advance this many ticks")
	all := fs.Bool("all", false, "compare every registered scenario")
	unmuted := fs.Bool("unmuted", false, "audible browser output (diagnostic)")
	var params StringList
	fs.Var(&params, "param", "scenario parameter key=value (repeatable)")
	if code, done := fs.parse(args); done {
		return nil, code, true
	}
	a := map[string]any{"all": *all}
	if ids := fs.Args(); len(ids) > 0 {
		a["ids"] = ids
	}
	if *seed != "" {
		a["seed"] = *seed
	}
	if *world != "" {
		a["world"] = *world
	}
	if *ticks >= 0 {
		a["ticks"] = *ticks
	}
	if *unmuted {
		a["unmuted"] = true
	}
	if pm, err := paramMap(params); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge scenario compare: %v\n", err)
		return nil, ExitUsage, true
	} else if len(pm) > 0 {
		a["params"] = pm
	}
	return &call{om: &om, op: "scenario.compare", args: a, stream: true, render: renderScenarioCompare}, 0, false
}

// cmdShot → visual.shot
func cmdShot(args []string) (*call, int, bool) {
	fs := cmdFlags("shot")
	var om outputMode
	om.register(fs)
	ticks := fs.Int("ticks", -1, "advance this many ticks")
	seed := fs.String("seed", "", "simulation seed")
	world := fs.String("world", "", "world seed")
	out := fs.String("out", "", "output PNG path")
	region := fs.String("region", "full", "named region")
	expr := fs.String("expr", "", "project JavaScript before capture")
	unmuted := fs.Bool("unmuted", false, "audible browser output (diagnostic)")
	lease := fs.Duration("lease", 10*time.Minute, "owned-resource lease")
	if code, done := fs.parse(args); done {
		return nil, code, true
	}
	a := map[string]any{"region": *region}
	if name := first(fs.Args()); name != "" {
		a["case"] = name
	}
	if *ticks >= 0 {
		a["ticks"] = *ticks
	}
	if *seed != "" {
		a["seed"] = *seed
	}
	if *world != "" {
		a["world"] = *world
	}
	if *out != "" {
		a["out"] = *out
	}
	if *expr != "" {
		a["expr"] = *expr
	}
	if *unmuted {
		a["unmuted"] = true
	}
	a["leaseMs"] = int(lease.Milliseconds())
	return &call{om: &om, op: "visual.shot", args: a, stream: true, render: renderShot}, 0, false
}

// cmdSweep → visual.sweep
func cmdSweep(args []string) (*call, int, bool) {
	fs := cmdFlags("sweep")
	var om outputMode
	om.register(fs)
	ticks := fs.Int("ticks", -1, "advance this many ticks per case")
	unmuted := fs.Bool("unmuted", false, "audible browser output (diagnostic)")
	if code, done := fs.parse(args); done {
		return nil, code, true
	}
	a := map[string]any{}
	if *ticks >= 0 {
		a["ticks"] = *ticks
	}
	if *unmuted {
		a["unmuted"] = true
	}
	return &call{om: &om, op: "visual.sweep", args: a, stream: true, render: renderSweep}, 0, false
}

// cmdPixel → visual.pixel
func cmdPixel(args []string) (*call, int, bool) {
	fs := cmdFlags("pixel")
	var om outputMode
	om.register(fs)
	caseName := fs.String("case", "", "visual case to capture")
	ticks := fs.Int("ticks", -1, "ticks before capture")
	seed := fs.String("seed", "", "seed preset or integer")
	world := fs.String("world", "", "world seed preset or integer")
	region := fs.String("region", "full", "named capture region")
	imagePath := fs.String("image", "", "sample an existing image")
	out := fs.String("out", "", "keep the capture at this path")
	if code, done := fs.parse(args); done {
		return nil, code, true
	}
	points, err := parsePoints(fs.Args())
	if err != nil || len(points) == 0 {
		if err != nil {
			fmt.Fprintf(os.Stderr, "game-forge pixel: %v\n", err)
		} else {
			fmt.Fprintln(os.Stderr, "game-forge pixel: at least one x,y coordinate is required")
		}
		return nil, ExitUsage, true
	}
	a := map[string]any{"points": points, "region": *region}
	if *caseName != "" {
		a["case"] = *caseName
	}
	if *ticks >= 0 {
		a["ticks"] = *ticks
	}
	if *seed != "" {
		a["seed"] = *seed
	}
	if *world != "" {
		a["world"] = *world
	}
	if *imagePath != "" {
		a["image"] = *imagePath
	}
	if *out != "" {
		a["out"] = *out
	}
	return &call{om: &om, op: "visual.pixel", args: a, stream: true, render: renderPixel}, 0, false
}

// cmdEval → browser.eval
func cmdEval(args []string) (*call, int, bool) {
	fs := cmdFlags("eval")
	var om outputMode
	om.register(fs)
	expr := fs.String("expr", "", "JavaScript expression to evaluate")
	caseName := fs.String("case", "", "load this visual case first")
	ticks := fs.Int("ticks", -1, "advance this many ticks")
	seed := fs.String("seed", "", "simulation seed")
	world := fs.String("world", "", "world seed")
	click := fs.String("click", "", "click this selector first")
	press := fs.String("press", "", "press this key first")
	reload := fs.Bool("reload", false, "reload the page first")
	unmuted := fs.Bool("unmuted", false, "audible browser output (diagnostic)")
	lease := fs.Duration("lease", 10*time.Minute, "owned-resource lease")
	if code, done := fs.parse(args); done {
		return nil, code, true
	}
	if *expr == "" {
		fmt.Fprintln(os.Stderr, "game-forge eval: --expr is required")
		return nil, ExitUsage, true
	}
	a := map[string]any{"expr": *expr}
	if *caseName != "" {
		a["case"] = *caseName
	}
	if *ticks >= 0 {
		a["ticks"] = *ticks
	}
	if *seed != "" {
		a["seed"] = *seed
	}
	if *world != "" {
		a["world"] = *world
	}
	if *click != "" {
		a["click"] = *click
	}
	if *press != "" {
		a["press"] = *press
	}
	if *reload {
		a["reload"] = true
	}
	if *unmuted {
		a["unmuted"] = true
	}
	a["leaseMs"] = int(lease.Milliseconds())
	return &call{om: &om, op: "browser.eval", args: a, stream: true, render: renderEval}, 0, false
}

// cmdErrors → browser.errors
func cmdErrors(args []string) (*call, int, bool) {
	fs := cmdFlags("errors")
	var om outputMode
	om.register(fs)
	caseName := fs.String("case", "", "load this visual case first")
	ticks := fs.Int("ticks", -1, "advance this many ticks")
	unmuted := fs.Bool("unmuted", false, "audible browser output (diagnostic)")
	if code, done := fs.parse(args); done {
		return nil, code, true
	}
	a := map[string]any{}
	if *caseName != "" {
		a["case"] = *caseName
	}
	if *ticks >= 0 {
		a["ticks"] = *ticks
	}
	if *unmuted {
		a["unmuted"] = true
	}
	return &call{om: &om, op: "browser.errors", args: a, stream: true, render: renderErrors}, 0, false
}

// cmdGPU → gpu.run
func cmdGPU(args []string) (*call, int, bool) {
	fs := cmdFlags("gpu")
	var om outputMode
	om.register(fs)
	scenarioName := fs.String("scenario", "", "benchmark scenario")
	ticks := fs.Int("ticks", -1, "advance this many ticks")
	frames := fs.Int("frames", -1, "benchmark frames")
	unmuted := fs.Bool("unmuted", false, "audible browser output (diagnostic)")
	if code, done := fs.parse(args); done {
		return nil, code, true
	}
	a := map[string]any{}
	if *scenarioName != "" {
		a["scenario"] = *scenarioName
	}
	if *ticks >= 0 {
		a["ticks"] = *ticks
	}
	if *frames >= 1 {
		a["frames"] = *frames
	}
	if *unmuted {
		a["unmuted"] = true
	}
	return &call{om: &om, op: "gpu.run", args: a, stream: true, render: renderGPU}, 0, false
}

// cmdVerify maps "verify [full]" to profile.run.
func cmdVerify(args []string) (*call, int, bool) {
	fs := cmdFlags("verify")
	var om outputMode
	om.register(fs)
	verbose := fs.Bool("verbose", false, "include stage output")
	unmuted := fs.Bool("unmuted", false, "audible browser output (diagnostic)")
	if code, done := fs.parse(args); done {
		return nil, code, true
	}
	name := "verify"
	extra := fs.Args()
	if len(extra) > 0 && extra[0] == "full" {
		name = "verify_full"
		extra = extra[1:]
	}
	return withOM(profileCall(name, extra, *verbose, *unmuted), &om), 0, false
}

// cmdProfileNamed maps a named profile invocation to profile.run.
func cmdProfileNamed(name string, args []string) (*call, int, bool) {
	fs := cmdFlags(name)
	var om outputMode
	om.register(fs)
	verbose := fs.Bool("verbose", false, "include stage output")
	unmuted := fs.Bool("unmuted", false, "audible browser output (diagnostic)")
	if code, done := fs.parse(args); done {
		return nil, code, true
	}
	return withOM(profileCall(name, fs.Args(), *verbose, *unmuted), &om), 0, false
}

// withOM attaches an output mode to a call.
func withOM(c *call, om *outputMode) *call {
	c.om = om
	return c
}

// profileCall builds the profile.run call.
func profileCall(name string, extra []string, verbose, unmuted bool) *call {
	a := map[string]any{"profile": name}
	if verbose {
		a["verbose"] = true
	}
	if unmuted {
		a["unmuted"] = true
	}
	if len(extra) > 0 {
		a["extra"] = extra
	}
	return &call{op: "profile.run", args: a, stream: true, render: renderProfileSummary}
}

// ---- helpers ----------------------------------------------------------------

func paramMap(list StringList) (map[string]string, error) {
	if len(list) == 0 {
		return nil, nil
	}
	m := map[string]string{}
	for _, p := range list {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--param expects key=value, got %q", p)
		}
		m[k] = v
	}
	return m, nil
}

func parsePoints(args []string) ([][2]int, error) {
	var out [][2]int
	for _, a := range args {
		xs, ys, ok := strings.Cut(a, ",")
		if !ok {
			return nil, fmt.Errorf("coordinate %q must be written x,y", a)
		}
		var x, y int
		if _, err := fmt.Sscanf(xs, "%d", &x); err != nil {
			return nil, fmt.Errorf("coordinate %q: %v", a, err)
		}
		if _, err := fmt.Sscanf(ys, "%d", &y); err != nil {
			return nil, fmt.Errorf("coordinate %q: %v", a, err)
		}
		out = append(out, [2]int{x, y})
	}
	return out, nil
}

func first(a []string) string {
	if len(a) == 0 {
		return ""
	}
	return a[0]
}
