// Package cli implements the game-forge command-line interface.
//
// The CLI is the first frontend over the Game Forge core. Future frontends
// (MCP, CI, scheduler) are expected to share the same core rather than shell
// out to this package.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rceman/game-forge/internal/browser"
	"github.com/rceman/game-forge/internal/config"
	"github.com/rceman/game-forge/internal/process"
	"github.com/rceman/game-forge/internal/project"
)

// Exit codes. These are stable: scripts and CI may rely on them.
const (
	ExitOK    = 0
	ExitFail  = 1
	ExitUsage = 2
)

// Version is the CLI version, overridable at build time.
var Version = "0.1.0-dev"

// Run executes the CLI and returns a process exit code.
func Run(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stdout)
		return ExitOK
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return ExitOK
	case "version", "-v", "--version":
		fmt.Printf("game-forge %s\n", Version)
		return ExitOK
	case "doctor":
		return cmdDoctor(rest)
	case "ps":
		return cmdPs(rest)
	case "gc":
		return cmdGC(rest)
	case "tick":
		return cmdTick(rest)
	case "scheduler":
		return cmdScheduler(rest)
	case "project":
		return cmdProject(rest)
	case "scenario":
		return cmdScenario(rest)
	case "shot":
		return cmdShot(rest)
	case "eval":
		return cmdEval(rest)
	case "sweep":
		return cmdSweep(rest)
	case "gpu":
		return cmdGPU(rest)
	case "test":
		return cmdProfile("test", rest)
	case "verify":
		return cmdProfile("verify", rest)
	case "build":
		return cmdProfile("build", rest)
	case "profile":
		name := ""
		if len(rest) > 0 {
			name = rest[0]
			rest = rest[1:]
		}
		if name == "" {
			fmt.Fprintln(os.Stderr, "game-forge profile: expected a profile name")
			return ExitUsage
		}
		return cmdProfile(name, rest)
	default:
		// Any name the project declares as a profile is runnable directly.
		if h, err := newHarness(); err == nil {
			if _, ok := h.m.Profile(cmd); ok {
				return cmdProfile(cmd, rest)
			}
		}
		fmt.Fprintf(os.Stderr, "game-forge: unknown command %q\n\n", cmd)
		printUsage(os.Stderr)
		return ExitUsage
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `game-forge - development and validation harness for games

Usage: game-forge <command> [options]

Project:
  project info                 Show the nearest project manifest
  scenario list                List the project's scenarios
  scenario describe <id>       Describe one scenario
  scenario run <id>            Run a scenario headlessly (--browser for the browser)
  scenario compare <id|--all>  Compare headless and browser authoritative output
  shot <case>                  Deterministic screenshot of a visual case
  sweep                        Load every visual case and report failures
  test                         Run the project's "test" profile
  verify [full]                Run the project's "verify" profile

Browser & GPU:
  doctor                       Validate configuration and the browser provider
  gpu                          Verify the real GPU renderer and benchmark

Resources:
  ps                           List resources owned by Game Forge
  gc                           Reclaim expired owned resources
  tick                         Scheduler entry point (idempotent cleanup)
  scheduler install|status|uninstall
                               Manage the per-user cleanup schedule

Other:
  version                      Print the Game Forge version
  help                         Show this help

Run "game-forge <command> -h" for command-specific options.
`)
}

// cmdDoctor validates machine configuration and the configured browser provider.
func cmdDoctor(args []string) int {
	fs := newFlagSet("doctor")
	leave := fs.Bool("no-cleanup", false, "leave the resource record for gc to reclaim")
	lease := fs.Duration("lease", 5*time.Minute, "resource lease before it becomes reclaimable")
	timeout := fs.Duration("timeout", 90*time.Second, "overall timeout")
	jsonOut := fs.Bool("json", false, "emit JSON")
	unmuted := fs.Bool("unmuted", false, "do not suppress browser audio output (diagnostic override)")
	if code, done := fs.parse(args); done {
		return code
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	cfg, path, err := config.LoadDefault()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge doctor: %v\n", err)
		return ExitFail
	}
	_, statErr := os.Stat(path)

	reg, err := openRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge doctor: %v\n", err)
		return ExitFail
	}

	runID := newRunID()
	ns := namespaceFor(cfg, runID)

	// Register ownership before touching the provider so an abandoned run is
	// always recoverable.
	res := &process.Resource{
		ID:        "browser-" + runID,
		RunID:     runID,
		Kind:      process.KindBrowser,
		Provider:  cfg.Browser.Provider,
		Namespace: ns,
		Host:      cfg.Browser.Host,
		Expires:   time.Now().UTC().Add(*lease),
	}
	if err := reg.Register(res); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge doctor: register resource: %v\n", err)
		return ExitFail
	}

	provider := browser.NewAgentBrowser(cfg, ns)
	if *unmuted {
		provider.SetUnmuted(true)
	}
	check, err := provider.Check(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge doctor: provider check: %v\n", err)
		return ExitFail
	}

	if !*leave {
		// Normal cleanup: release the provider session, then drop the record.
		_ = provider.Close(context.Background())
		if err := reg.Remove(res.ID); err != nil {
			fmt.Fprintf(os.Stderr, "game-forge doctor: remove resource: %v\n", err)
		}
	}

	if *jsonOut {
		report := map[string]any{
			"config_path":   path,
			"config_exists": statErr == nil,
			"check":         check,
			"namespace":     ns,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
	} else {
		printDoctor(path, statErr == nil, check, ns, *leave)
	}

	if check.OK {
		return ExitOK
	}
	return ExitFail
}

func printDoctor(path string, exists bool, check *browser.CheckResult, ns string, left bool) {
	fmt.Println("game-forge doctor")
	state := "not found (using defaults)"
	if exists {
		state = "found"
	}
	fmt.Printf("  config:   %s (%s)\n", path, state)
	fmt.Printf("  provider: %s (host: %s)\n", check.Provider, check.Host)
	for _, d := range check.Details {
		fmt.Printf("    - %s\n", d)
	}
	if left {
		fmt.Printf("  resource: left registered (namespace %s) for gc\n", ns)
	} else {
		fmt.Println("  resource: registered then released (normal cleanup)")
	}
	if len(check.Problems) > 0 {
		fmt.Println("  problems:")
		for _, p := range check.Problems {
			fmt.Printf("    ! %s\n", p)
		}
		fmt.Println("  status:   FAIL")
		return
	}
	fmt.Println("  status:   OK")
}

// cmdPs lists resources owned by Game Forge.
func cmdPs(args []string) int {
	fs := newFlagSet("ps")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if code, done := fs.parse(args); done {
		return code
	}
	reg, err := openRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge ps: %v\n", err)
		return ExitFail
	}
	all, err := reg.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge ps: %v\n", err)
		return ExitFail
	}
	now := time.Now().UTC()
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(all)
		return ExitOK
	}
	if len(all) == 0 {
		fmt.Println("no owned resources")
		return ExitOK
	}
	fmt.Printf("%-28s %-8s %-14s %-24s %-8s\n", "ID", "KIND", "PROVIDER", "NAMESPACE", "STATE")
	for _, r := range all {
		st := "active"
		if r.Expired(now) {
			st = "expired"
		}
		fmt.Printf("%-28s %-8s %-14s %-24s %-8s\n", r.ID, r.Kind, r.Provider, r.Namespace, st)
	}
	return ExitOK
}

// cmdGC reclaims expired owned resources.
func cmdGC(args []string) int {
	fs := newFlagSet("gc")
	timeout := fs.Duration("timeout", 60*time.Second, "overall timeout")
	if code, done := fs.parse(args); done {
		return code
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	return runReclaim(ctx, false)
}

// runReclaim reclaims every expired owned resource. It is idempotent: a second
// run finds nothing to do.
func runReclaim(ctx context.Context, quiet bool) int {
	cfg, _, err := config.LoadDefault()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge: %v\n", err)
		return ExitFail
	}
	reg, err := openRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge: %v\n", err)
		return ExitFail
	}
	expired, err := reg.Expired(time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge: %v\n", err)
		return ExitFail
	}
	if len(expired) == 0 {
		if !quiet {
			fmt.Println("nothing to reclaim")
		}
		return ExitOK
	}
	h := &harness{cfg: cfg, reg: reg}
	failed := false
	for _, r := range expired {
		fmt.Printf("reclaiming %s (%s %s namespace=%s)\n", r.ID, r.Provider, r.Kind, r.Namespace)
		if err := h.reclaimResource(ctx, r); err != nil {
			fmt.Fprintf(os.Stderr, "  ! %v\n", err)
			failed = true
			continue
		}
		if err := reg.Remove(r.ID); err != nil {
			fmt.Fprintf(os.Stderr, "  ! remove record: %v\n", err)
			failed = true
		}
	}
	if failed {
		return ExitFail
	}
	return ExitOK
}

// cmdProject shows the nearest project manifest.
func cmdProject(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "game-forge project: expected subcommand (info)")
		return ExitUsage
	}
	switch args[0] {
	case "info":
		return cmdProjectInfo(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "game-forge project: unknown subcommand %q\n", args[0])
		return ExitUsage
	}
}

func cmdProjectInfo(args []string) int {
	fs := newFlagSet("project info")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if code, done := fs.parse(args); done {
		return code
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge project info: %v\n", err)
		return ExitFail
	}
	m, err := project.DiscoverAndLoad(wd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge project info: %v\n", err)
		return ExitFail
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(m)
		return ExitOK
	}
	fmt.Println("game-forge project")
	fmt.Printf("  root:       %s\n", m.Root)
	fmt.Printf("  id:         %s\n", m.Project.ID)
	fmt.Printf("  type:       %s\n", m.Project.Type)
	fmt.Printf("  contract:   %s\n", m.Contract)
	if len(m.Capabilities) > 0 {
		var caps []string
		for k, v := range m.Capabilities {
			if v {
				caps = append(caps, k)
			}
		}
		fmt.Printf("  capabilities: %s\n", strings.Join(caps, ", "))
	}
	if len(m.Adapters) > 0 {
		fmt.Println("  adapters:")
		for name, a := range m.Adapters {
			fmt.Printf("    - %s: %s (%s)\n", name, strings.Join(a.Command, " "), a.Protocol)
		}
	}
	return ExitOK
}

// openRegistry opens the durable resource registry.
func openRegistry() (*process.Registry, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	return process.Open(filepath.Join(dir, "state", "resources"))
}

// namespaceFor builds an agent-browser namespace from the config prefix.
func namespaceFor(cfg *config.Config, runID string) string {
	prefix := cfg.Browser.AgentBrowser.NamespacePrefix
	if prefix == "" {
		prefix = "game-forge"
	}
	return prefix + "-" + runID
}

// newRunID returns a sortable, unique run identifier.
func newRunID() string {
	return fmt.Sprintf("%d-%d", time.Now().UTC().Unix(), os.Getpid())
}
