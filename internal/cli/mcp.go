package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rceman/game-forge/internal/client"
	"github.com/rceman/game-forge/internal/daemon"
	"github.com/rceman/game-forge/internal/mcpfrontend"
	"github.com/rceman/game-forge/internal/op"
	"github.com/rceman/game-forge/internal/project"
)

// httpDispatcher adapts the daemon HTTP client to the MCP dispatcher
// contract, so the stdio compatibility frontend runs the identical /v1/run
// pipeline — project_code included — as the daemon's in-process /mcp handler.
type httpDispatcher struct{ cl *client.Client }

// Catalog implements mcpfrontend.Dispatcher.
func (h httpDispatcher) Catalog(ctx context.Context) (string, int, []op.Meta, error) {
	c, err := h.cl.Catalog(ctx)
	if err != nil {
		return "", 0, nil, err
	}
	return c.Protocol, c.V, c.Ops, nil
}

// Run implements mcpfrontend.Dispatcher.
func (h httpDispatcher) Run(ctx context.Context, req *op.Request, sink op.Sink) (any, *op.Error) {
	return h.cl.RunRequest(ctx, req, sink)
}

// Requests implements mcpfrontend.Dispatcher.
func (h httpDispatcher) Requests() int64 { return h.cl.Requests() }

// cmdMCP owns the MCP frontend commands. The canonical transport is the
// daemon's /mcp streamable-HTTP endpoint; `mcp serve` is the stdio
// compatibility frontend over the same dispatcher and the same per-call
// project_code routing. `mcp info` reports the endpoint and (explicitly) the
// durable credential; `mcp audit` measures the efficiency surface.
func cmdMCP(args []string) int {
	switch first(args) {
	case "serve":
		return mcpServe(args[1:])
	case "info":
		return mcpInfo(args[1:])
	case "audit":
		return mcpAudit(args[1:])
	default:
		fmt.Fprintln(os.Stderr, "game-forge mcp: expected subcommand (serve, info, audit)")
		return ExitUsage
	}
}

// mcpServe runs the stdio MCP compatibility frontend. It uses the same daemon
// dispatcher and the same project_code semantics as /mcp — there is no
// cwd-pinned project identity and no per-project process.
func mcpServe(args []string) int {
	var opt mcpfrontend.Options
	for _, a := range args {
		switch a {
		case "--compat-text":
			// Mirror the full JSON result into TextContent for MCP clients
			// that predate structuredContent. Off by default: the structured
			// result is authoritative and duplicating it doubles context cost.
			opt.CompatText = true
		case "--full-schemas":
			// Advertise canonical output schemas in tools/list. Off by
			// default: name + description + input schema are what a model
			// needs to choose and call a tool.
			opt.FullSchemas = true
		}
	}
	ctx := context.Background()
	cl, err := client.Ensure(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge mcp serve: %v\n", err)
		return ExitFail
	}
	mcpfrontend.Version = Version
	fe, err := mcpfrontend.New(ctx, httpDispatcher{cl}, opt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge mcp serve: %v\n", err)
		return ExitFail
	}
	if err := fe.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintf(os.Stderr, "game-forge mcp serve: %v\n", err)
		return ExitFail
	}
	return ExitOK
}

// mcpInfo reports the canonical MCP endpoint and daemon state. The durable
// MCP credential is only printed under --show-token — an explicit
// credential-revealing command, never incidental output.
func mcpInfo(args []string) int {
	var asJSON, showToken bool
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "--show-token":
			showToken = true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The endpoint is durable: report it whether or not the daemon is up.
	port, _ := daemon.LoadEndpointPort()
	endpoint := ""
	if port != 0 {
		endpoint = fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	}
	cl := client.Connect(ctx)
	running := cl != nil
	pid, ops := 0, 0
	if running {
		d := cl.Discovery()
		pid = d.PID
		if l, err := cl.Capabilities(ctx); err == nil {
			ops = len(l)
		}
		endpoint = d.Endpoint + "/mcp"
	}
	if asJSON {
		v := map[string]any{
			"endpoint":  endpoint,
			"transport": "streamable-http",
			"daemon":    map[string]any{"running": running, "pid": pid, "ops": ops},
		}
		if showToken {
			if tok, err := daemon.LoadMCPToken(); err == nil && tok != "" {
				v["token"] = tok
			}
		}
		data, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(data))
		return ExitOK
	}
	fmt.Println("Game Forge MCP")
	fmt.Printf("  endpoint:   %s\n", endpoint)
	fmt.Printf("  transport:  streamable-http\n")
	if running {
		fmt.Printf("  daemon:     running (pid %d, %d ops)\n", pid, ops)
	} else {
		fmt.Println("  daemon:     not running")
	}
	fmt.Println("  auth:       bearer (durable credential; see --json --show-token)")
	if showToken {
		if tok, err := daemon.LoadMCPToken(); err == nil && tok != "" {
			fmt.Printf("  token:      %s\n", tok)
		} else {
			fmt.Println("  token:      (not yet created — daemon has never run)")
		}
	}
	return ExitOK
}

// mcpAudit measures the MCP efficiency contract — catalog size, model surface,
// project_code overhead, initialization round trips and latency — against the
// checked-in budget. It never invokes a browser/server/GPU tool. When a
// project is registered the probe call routes through it; otherwise the probe
// measures the bounded unknown_project error path.
func mcpAudit(args []string) int {
	var asJSON bool
	for _, a := range args {
		if a == "--json" {
			asJSON = true
		}
	}
	ctx := context.Background()
	cl, err := client.Ensure(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge mcp audit: %v\n", err)
		return ExitFail
	}
	// Pick a registered project for the probe so the success path is
	// measured when one exists.
	probeCode := ""
	if reg, err := project.OpenRegistry(); err == nil {
		if list := reg.List(); len(list) > 0 {
			probeCode = list[0].Code
		}
	}
	rep, err := mcpfrontend.Audit(ctx, httpDispatcher{cl}, probeCode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge mcp audit: %v\n", err)
		return ExitFail
	}
	if asJSON {
		data, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "game-forge mcp audit: %v\n", err)
			return ExitFail
		}
		fmt.Fprintln(os.Stdout, string(data))
		if rep.Status == "PASS" {
			return ExitOK
		}
		return ExitFail
	}
	return rep.Render(os.Stdout)
}
