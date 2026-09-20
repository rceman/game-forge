package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rceman/game-forge/internal/client"
	"github.com/rceman/game-forge/internal/mcpfrontend"
)

// cmdMCP owns the MCP frontend. `mcp serve` speaks MCP over stdio — stdout is
// the protocol channel, so every diagnostic goes to stderr. `mcp audit`
// measures the MCP efficiency surface against the checked-in budget.
func cmdMCP(args []string) int {
	switch first(args) {
	case "serve":
		return mcpServe(args[1:])
	case "audit":
		return mcpAudit(args[1:])
	default:
		fmt.Fprintln(os.Stderr, "game-forge mcp: expected subcommand (serve, audit)")
		return ExitUsage
	}
}

// mcpServe runs the stdio MCP frontend. It pins one project cwd — the process
// cwd by default, or --cwd — and uses the same daemon client as the CLI, so
// the per-user daemon stays the single lifecycle owner and no second
// resource-owning Runtime is created.
func mcpServe(args []string) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge mcp serve: %v\n", err)
		return ExitFail
	}
	var opt mcpfrontend.Options
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cwd":
			if i+1 < len(args) {
				cwd = args[i+1]
				i++
			}
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
	fe, err := mcpfrontend.NewWithOptions(ctx, cl, cwd, opt)
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

// mcpAudit measures the MCP efficiency contract — catalog size, model surface,
// initialization round trips and latency — against the checked-in budget. It
// is metadata-only: it never invokes a tool, so no browser, dev server or GPU
// probe is started and no project is required.
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
	rep, err := mcpfrontend.Audit(ctx, cl)
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
