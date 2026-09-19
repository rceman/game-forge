package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rceman/game-forge/internal/client"
	"github.com/rceman/game-forge/internal/mcpfrontend"
)

// cmdMCP owns the MCP frontend. `mcp serve` speaks MCP over stdio — stdout is
// the protocol channel, so every diagnostic goes to stderr.
func cmdMCP(args []string) int {
	if first(args) != "serve" {
		fmt.Fprintln(os.Stderr, "game-forge mcp: expected subcommand (serve)")
		return ExitUsage
	}
	return mcpServe(args[1:])
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
	for i := 0; i < len(args); i++ {
		if args[i] == "--cwd" && i+1 < len(args) {
			cwd = args[i+1]
			i++
		}
	}
	ctx := context.Background()
	cl, err := client.Ensure(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge mcp serve: %v\n", err)
		return ExitFail
	}
	mcpfrontend.Version = Version
	fe, err := mcpfrontend.New(ctx, cl, cwd)
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
