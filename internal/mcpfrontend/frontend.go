// Package mcpfrontend is the MCP frontend for Game Forge.
//
// It is deliberately thin: it exposes the daemon's canonical operations as MCP
// tools, translates MCP calls into Game Forge operation requests, and maps
// results/errors/progress back. It owns no browser, server, registry or
// operation semantics — the per-user daemon remains the single lifecycle
// owner, and the Operation Registry remains the single source of tool
// metadata and schemas.
//
//	MCP client --stdio--> game-forge mcp serve --daemon client--> game-forged
//	  -> Operation Registry -> Core
package mcpfrontend

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rceman/game-forge/internal/client"
	"github.com/rceman/game-forge/internal/daemon"
	"github.com/rceman/game-forge/internal/op"
	"github.com/rceman/game-forge/schemas"
)

// Version is the MCP server implementation version reported to clients,
// overridable at build time.
var Version = "0.1.0-dev"

// Frontend is a Game Forge MCP server bound to one daemon client and one
// project cwd.
type Frontend struct {
	cl  *client.Client
	srv *mcp.Server
	// ops maps an MCP tool name to its canonical operation name.
	ops map[string]string
	// metas records the canonical op metadata each tool was built from, for
	// diagnostics and tests.
	metas map[string]*client.OpSchema
}

// New connects to the daemon, verifies protocol compatibility, discovers the
// canonical operation catalog and registers one MCP tool per operation. cwd is
// the project context forwarded in every request envelope.
func New(ctx context.Context, cl *client.Client, cwd string) (*Frontend, error) {
	if cl == nil {
		return nil, fmt.Errorf("game-forged client is required")
	}
	if err := checkDaemon(ctx, cl); err != nil {
		return nil, err
	}
	cl = cl.WithCwd(cwd)
	names, err := cl.Capabilities(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover operations: %w", err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "game-forge", Version: Version}, nil)
	f := &Frontend{cl: cl, srv: srv, ops: map[string]string{}, metas: map[string]*client.OpSchema{}}
	for _, name := range names {
		meta, err := cl.Schema(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("schema %s: %w", name, err)
		}
		if err := f.add(name, meta); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// checkDaemon verifies the connected daemon speaks the protocol this frontend
// was built against. Tools are derived from the daemon's own schema endpoint,
// so within a protocol version the exposed catalog can never drift from what
// the daemon executes; a protocol mismatch is a hard failure.
func checkDaemon(ctx context.Context, cl *client.Client) error {
	h, err := cl.HealthCheck(ctx)
	if err != nil {
		return fmt.Errorf("daemon health: %w", err)
	}
	if !h.OK || h.Protocol != daemon.Protocol || h.V != schemas.Version {
		return fmt.Errorf("incompatible game-forged (protocol %q v%d; want %q v%d) — run: game-forge daemon restart",
			h.Protocol, h.V, daemon.Protocol, schemas.Version)
	}
	return nil
}

// toolName maps a canonical operation name to an MCP tool name. Dots become
// underscores (tool names must match [a-zA-Z0-9_-]{1,64}); the mapping is
// deterministic and one-to-one for the registered operations.
func toolName(opName string) string {
	return strings.ReplaceAll(opName, ".", "_")
}

// add registers one operation as an MCP tool. Schemas come verbatim from the
// daemon's canonical contract; no MCP-specific schema is ever synthesized.
func (f *Frontend) add(opName string, meta *client.OpSchema) error {
	tn := toolName(opName)
	if prev, dup := f.ops[tn]; dup {
		return fmt.Errorf("tool name collision: %q and %q both map to %q", prev, opName, tn)
	}
	if !isObjectSchema(meta.Input) {
		return fmt.Errorf("operation %s: input schema is not an object schema", opName)
	}
	t := &mcp.Tool{
		Name:        tn,
		Description: meta.Summary,
		InputSchema: json.RawMessage(meta.Input),
	}
	if isObjectSchema(meta.Output) {
		t.OutputSchema = json.RawMessage(meta.Output)
	}
	f.ops[tn] = opName
	f.metas[opName] = meta
	f.srv.AddTool(t, f.call(opName))
	return nil
}

// isObjectSchema reports whether a raw JSON Schema has "type":"object".
// MCP requires tool input/output schemas to be objects.
func isObjectSchema(raw json.RawMessage) bool {
	var v struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &v) == nil && v.Type == "object"
}

// OpName returns the canonical operation behind an MCP tool name, for
// diagnostics.
func (f *Frontend) OpName(tool string) string { return f.ops[tool] }

// Run serves the frontend over transport t until it ends or ctx is canceled.
func (f *Frontend) Run(ctx context.Context, t mcp.Transport) error {
	return f.srv.Run(ctx, t)
}

// call builds the MCP handler for one canonical operation. A tool call becomes
// a Game Forge request envelope and is executed through the daemon client —
// never a shell command, never a second Core.
func (f *Frontend) call(opName string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var token any
		if req.Params != nil {
			token = req.Params.GetProgressToken()
		}
		var stage int
		// Semantic progress only: Game Forge stage/artifact events become MCP
		// progress notifications when the client supplied a progress token.
		// Raw subprocess logs are never forwarded.
		onEvent := func(ev op.Event) {
			if token == nil || req.Session == nil {
				return
			}
			var msg string
			switch ev.Ev {
			case op.EvStage:
				stage++
				msg = ev.Name + " " + ev.Status
				if s, _ := ev.Data.(string); s != "" {
					msg += " " + s
				}
			case op.EvArtifact:
				msg = "artifact " + ev.Kind + " " + ev.Path
			default:
				return
			}
			_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token,
				Progress:      float64(stage),
				Message:       msg,
			})
		}

		var args json.RawMessage
		if req.Params != nil && len(req.Params.Arguments) > 0 {
			args = req.Params.Arguments
		}
		// Streaming is used for every call so stage/artifact events flow into
		// progress; the operation's canonical result is the done event's data.
		// ctx cancellation propagates to the daemon request, canceling the Core
		// operation without touching reusable daemon-owned resources.
		data, werr := f.cl.Stream(ctx, opName, args, onEvent)
		if werr != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: errText(werr)}},
			}, nil
		}
		res := &mcp.CallToolResult{}
		if len(data) > 0 && string(data) != "null" {
			// Structured content is authoritative; the compact JSON text is
			// the standard fallback for clients without structured-content
			// support.
			res.StructuredContent = json.RawMessage(data)
			res.Content = []mcp.Content{&mcp.TextContent{Text: string(data)}}
		}
		return res, nil
	}
}

// errText renders a canonical Game Forge error concisely: code, message and
// the arg path when the failure is argument-shaped. Tokens and internal auth
// details are never included.
func errText(e *op.Error) string {
	s := e.Code + ": " + e.Msg
	if e.Path != "" {
		s += " (" + e.Path + ")"
	}
	return s
}
