// Package cli implements the game-forge command-line interface.
//
// The CLI is one frontend over the Operation Registry. It parses familiar
// syntax into an operation request, transparently ensures the daemon is
// running, sends the request over loopback HTTP, and renders the result. It
// never implements operation semantics: the daemon's Core does.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/rceman/game-forge/internal/client"
	"github.com/rceman/game-forge/internal/op"
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
	switch args[0] {
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return ExitOK
	case "version", "-v", "--version":
		fmt.Printf("game-forge %s\n", Version)
		return ExitOK
	case "daemon":
		return cmdDaemon(args[1:])
	case "mcp":
		return cmdMCP(args[1:])
	case "project":
		// The registry subcommands are local machine state; "project info"
		// remains the daemon operation.
		if len(args) > 1 {
			switch args[1] {
			case "add", "list", "show", "remove":
				return cmdRegistry(args[1:])
			}
		}
	}
	c, code, done := dispatch(args)
	if done {
		return code
	}
	return runCall(c)
}

// runCall ensures the daemon, sends the operation and renders the result.
func runCall(c *call) int {
	ctx := context.Background()
	cl, err := client.Ensure(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge: %v\n", err)
		return ExitFail
	}
	if c.om != nil && c.om.ndjson {
		return runNDJSON(ctx, cl, c)
	}
	if c.om != nil && c.om.json {
		return runJSON(ctx, cl, c)
	}
	return runHuman(ctx, cl, c)
}

// runHuman executes an operation and renders its result for a person. A
// streaming operation reports live stage progress; the final data drives the
// human summary.
func runHuman(ctx context.Context, cl *client.Client, c *call) int {
	var data json.RawMessage
	var werr *op.Error
	if c.stream {
		data, werr = cl.Stream(ctx, c.op, c.args, func(ev op.Event) {
			if ev.Ev == op.EvStage && ev.Name != "" {
				detail, _ := ev.Data.(string)
				line := fmt.Sprintf("%-5s %-24s %s", statusWord(ev.Status), ev.Name, fmtMS(ev.MS))
				if detail != "" {
					line += " " + detail
				}
				fmt.Fprintln(os.Stdout, line)
			}
		})
	} else {
		data, werr = cl.Run(ctx, c.op, c.args)
	}
	return finish(c, data, werr)
}

// runJSON executes an operation and prints its result as JSON.
func runJSON(ctx context.Context, cl *client.Client, c *call) int {
	data, werr := cl.Run(ctx, c.op, c.args)
	if len(data) > 0 {
		var v any
		_ = json.Unmarshal(data, &v)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
	}
	return finish(c, nil, werr)
}

// runNDJSON echoes the raw event stream.
func runNDJSON(ctx context.Context, cl *client.Client, c *call) int {
	var werr *op.Error
	_, werr = cl.Stream(ctx, c.op, c.args, func(ev op.Event) {
		raw, _ := json.Marshal(ev)
		fmt.Fprintln(os.Stdout, string(raw))
	})
	return exitFor(werr)
}

// finish renders the result and maps the wire error to an exit code.
func finish(c *call, data json.RawMessage, werr *op.Error) int {
	code := exitFor(werr)
	if len(data) > 0 && c.render != nil {
		if rc := c.render(os.Stdout, data); rc != ExitOK && code == ExitOK {
			code = rc
		}
	} else if werr != nil {
		fmt.Fprintf(os.Stderr, "game-forge %s: %s\n", c.op, werr.Msg)
	}
	return code
}

// exitFor maps a wire error to a process exit code.
func exitFor(e *op.Error) int {
	if e == nil {
		return ExitOK
	}
	switch e.Code {
	case op.CodeInvalidArgs, op.CodeInvalidRequest, op.CodeUnsupported, op.CodeUnknownOp:
		return ExitUsage
	}
	return ExitFail
}

// statusWord maps a stage status to a display word.
func statusWord(s string) string {
	if s == op.StatusPass {
		return "PASS"
	}
	return "FAIL"
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `game-forge - development and validation harness for games

Usage: game-forge <command> [options]

Ordinary commands run through the local Game Forge daemon, which is started
automatically. Most support --json for a structured result and --ndjson for
the raw event stream.

Project:
  project info                 Show the nearest project manifest
  project add <code>           Register the current project under a stable code
  project list|show|remove     Manage the machine-local project registry
  scenario list                List the project's scenarios
  scenario describe <id>       Describe one scenario
  scenario run <id>            Run a scenario headlessly (--browser for the browser)
  scenario compare <id|--all>  Compare headless and browser authoritative output
  shot <case>                  Deterministic screenshot of a visual case
  sweep                        Load every visual case and report failures
  pixel <x,y> ...              Sample real rendered pixels
  eval                         Evaluate a project expression in the browser
  errors                       Report page/console diagnostics
  test [-- <filter>]           Run the project's "test" profile
  verify [full]                Run the project's "verify" profile

Browser & GPU:
  doctor                       Validate configuration and the browser provider
  gpu                          Verify the real GPU renderer and benchmark

Resources:
  serve start|status|stop      Manage the declared dev/prod server
  ps                           List resources owned by Game Forge
  gc                           Reclaim expired owned resources
  tick                         One idempotent housekeeping pass

Daemon:
  daemon status                Show the running daemon
  daemon stop                  Stop the daemon gracefully
  daemon restart               Restart the daemon
  daemon rebind                Pick a new durable port (MCP endpoint changes)

MCP:
  mcp info [--json] [--show-token]  Show the canonical MCP endpoint
  mcp serve                  Serve MCP over stdio (compatibility frontend)
  mcp audit [--json]           Measure the MCP efficiency surface vs budget

Other:
  version                      Print the Game Forge version
  help                         Show this help

Run "game-forge <command> -h" for command-specific options.
`)
}
