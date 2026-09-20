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
//
// Efficiency is part of correctness for an agent-facing interface: the
// frontend keeps the model-facing surface dense by serving compact tool
// definitions, omitting output schemas (the daemon still validates results
// canonically), never duplicating a structured result into text content, and
// deduplicating progress.
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

// Wire-efficiency bounds enforced by the frontend itself. The durable numeric
// budget lives in efficiency-budget.json; these constants bound data the
// frontend emits per call regardless of catalog size.
const (
	// maxErrorText bounds a tool-call error. Canonical errors are already
	// "code: msg (path)"; the cap exists so an unexpected giant message
	// (e.g. subprocess output embedded in a failure) cannot flood context.
	maxErrorText = 512
	// maxProgressText bounds one progress message so a noisy stage detail
	// cannot become log spam. Stage details are short by contract.
	maxProgressText = 200
)

// Options tunes the frontend's compatibility surface. The zero value is the
// compact, agent-oriented mode.
type Options struct {
	// CompatText mirrors the full JSON result into TextContent for clients
	// that predate structuredContent. Compact mode leaves content empty
	// because the structured result is authoritative — duplicating it would
	// double every result's context cost.
	CompatText bool
	// FullSchemas advertises each tool's canonical output schema. Compact
	// mode omits output schemas from tools/list: the model needs name,
	// description and input schema to choose and call a tool, and daemon-side
	// output validation is unchanged either way.
	FullSchemas bool
}

// Frontend is a Game Forge MCP server bound to one daemon client and one
// project cwd.
type Frontend struct {
	cl  *client.Client
	srv *mcp.Server
	opt Options
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
	return NewWithOptions(ctx, cl, cwd, Options{})
}

// NewWithOptions is New with explicit compatibility options.
func NewWithOptions(ctx context.Context, cl *client.Client, cwd string, opt Options) (*Frontend, error) {
	if cl == nil {
		return nil, fmt.Errorf("game-forged client is required")
	}
	cl = cl.WithCwd(cwd)
	// ONE catalog request carries both the compatibility identity and every
	// operation's contract — initialization cost does not grow with tool
	// count. This request is metadata-only: it never starts a browser, a dev
	// server or a GPU probe.
	cat, err := cl.Catalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover operations: %w", err)
	}
	if cat.Protocol != daemon.Protocol || cat.V != schemas.Version {
		return nil, fmt.Errorf("incompatible game-forged (protocol %q v%d; want %q v%d) — run: game-forge daemon restart",
			cat.Protocol, cat.V, daemon.Protocol, schemas.Version)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "game-forge", Version: Version}, nil)
	f := &Frontend{cl: cl, srv: srv, opt: opt, ops: map[string]string{}, metas: map[string]*client.OpSchema{}}
	for i := range cat.Ops {
		meta := &cat.Ops[i]
		if err := f.add(meta.Op, meta); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// toolName maps a canonical operation name to an MCP tool name. Dots become
// underscores (tool names must match [a-zA-Z0-9_-]{1,64}); the mapping is
// deterministic and one-to-one for the registered operations.
func toolName(opName string) string {
	return strings.ReplaceAll(opName, ".", "_")
}

// add registers one operation as an MCP tool. Tool schemas are a deterministic
// compact projection of the canonical contract — never a second handwritten
// schema — so the exposed contract cannot drift from what the daemon
// validates and executes.
func (f *Frontend) add(opName string, meta *client.OpSchema) error {
	tn := toolName(opName)
	if prev, dup := f.ops[tn]; dup {
		return fmt.Errorf("tool name collision: %q and %q both map to %q", prev, opName, tn)
	}
	in := compactSchema(meta.Input)
	if !isObjectSchema(in) {
		return fmt.Errorf("operation %s: input schema is not an object schema", opName)
	}
	t := &mcp.Tool{
		Name:        tn,
		Description: meta.Summary,
		InputSchema: json.RawMessage(in),
	}
	if f.opt.FullSchemas {
		if out := compactSchema(meta.Output); isObjectSchema(out) {
			t.OutputSchema = json.RawMessage(out)
		}
	}
	f.ops[tn] = opName
	f.metas[opName] = meta
	f.srv.AddTool(t, f.call(opName))
	return nil
}

// compactSchema derives the MCP-advertised schema from the canonical one by
// dropping metadata that carries no validation semantics: "$schema" (draft
// marker) and "$id" (document URI). All other keys — type, required,
// properties, enum, bounds, additionalProperties, property descriptions — are
// preserved verbatim because they either constrain arguments or help an agent
// choose values. If a schema ever grows "$ref" anchors the projection is
// disabled for that document, since $id then participates in resolution.
func compactSchema(raw json.RawMessage) json.RawMessage {
	var doc any
	if json.Unmarshal(raw, &doc) != nil {
		return raw
	}
	if containsRef(doc) {
		return raw
	}
	stripMeta(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}

// containsRef reports whether a "$ref" key appears anywhere in the document.
func containsRef(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		if list, isList := v.([]any); isList {
			for _, e := range list {
				if containsRef(e) {
					return true
				}
			}
		}
		return false
	}
	if _, has := m["$ref"]; has {
		return true
	}
	for _, e := range m {
		if containsRef(e) {
			return true
		}
	}
	return false
}

// stripMeta removes wire-irrelevant schema metadata recursively.
func stripMeta(v any) {
	m, ok := v.(map[string]any)
	if !ok {
		if list, isList := v.([]any); isList {
			for _, e := range list {
				stripMeta(e)
			}
		}
		return
	}
	delete(m, "$schema")
	delete(m, "$id")
	for _, e := range m {
		stripMeta(e)
	}
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
		var lastMsg string
		// Semantic progress only: Game Forge stage/artifact events become MCP
		// progress notifications when the client supplied a progress token.
		// Raw subprocess logs are never forwarded, and a repeated identical
		// message is suppressed rather than re-emitted.
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
			if msg == lastMsg {
				return
			}
			lastMsg = msg
			if len(msg) > maxProgressText {
				msg = msg[:maxProgressText]
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
		res := &mcp.CallToolResult{Content: []mcp.Content{}}
		if len(data) > 0 && string(data) != "null" {
			// The canonical structured result is authoritative. Compact mode
			// emits no mirrored text: a standards-valid empty content array
			// costs nothing in context. Compat mode mirrors the JSON for
			// clients that predate structuredContent.
			res.StructuredContent = json.RawMessage(data)
			if f.opt.CompatText {
				res.Content = []mcp.Content{&mcp.TextContent{Text: string(data)}}
			}
		}
		return res, nil
	}
}

// errText renders a canonical Game Forge error concisely: code, message and
// the arg path when the failure is argument-shaped. It is hard-capped so a
// giant embedded message cannot flood context. Tokens and internal auth
// details are never included.
func errText(e *op.Error) string {
	s := e.Code + ": " + e.Msg
	if e.Path != "" {
		s += " (" + e.Path + ")"
	}
	if len(s) > maxErrorText {
		s = s[:maxErrorText-3] + "..."
	}
	return s
}
