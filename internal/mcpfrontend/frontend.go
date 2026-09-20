// Package mcpfrontend is the thin MCP adapter over the Operation Registry.
//
// The MCP frontend never implements operation semantics and never owns a
// second Core/runtime: it asks a Dispatcher (the daemon's in-process
// dispatcher on the canonical /mcp endpoint, or the daemon HTTP client for
// stdio compatibility) for the canonical operation catalog and forwards each
// tool call to the daemon, which remains the canonical validator and the sole
// owner of project, browser and server resources.
//
// Project selection is transport metadata: every tool accepts a required
// "project_code" argument that resolves against the durable project registry.
// The frontend extracts it before canonical argument validation and forwards
// it on the request envelope — canonical operation schemas never see it.
// There are no sessions or hidden current-project state; every call names
// its project, so concurrent calls for different projects never cross.
package mcpfrontend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rceman/game-forge/internal/op"
	"github.com/rceman/game-forge/schemas"
)

// Version is the MCP server implementation version, set by the CLI at startup.
var Version = "dev"

// maxErrorText caps an MCP error result; the audit budget asserts it.
const maxErrorText = 512

// maxProgressText caps one progress message; raw logs are never forwarded,
// only the semantic stage/artifact transitions below.
const maxProgressText = 200

// Options controls the MCP wire surface.
type Options struct {
	// CompatText mirrors the structured result into a TextContent entry for
	// clients that cannot read structuredContent. Default mode emits no text
	// so the same JSON is never transmitted twice.
	CompatText bool
	// FullSchemas advertises canonical output schemas on every tool. Default
	// mode omits them: a model needs name+description+input schema to choose
	// and call a tool, and the daemon still validates every result
	// canonically — the output schema is not needed to pick or call.
	FullSchemas bool
}

// Dispatcher is how a frontend reaches the Operation Registry/Core without
// owning resources itself. The daemon's in-process dispatcher (canonical /mcp
// endpoint) and the daemon HTTP client (stdio compatibility) both satisfy it.
type Dispatcher interface {
	// Catalog returns the protocol identity and every operation's canonical
	// contract — one call, regardless of how many operations exist.
	Catalog(ctx context.Context) (protocol string, version int, ops []op.Meta, err error)
	// Run executes one canonical request envelope. req.Project carries the
	// registered project code; the daemon resolves it to a root.
	Run(ctx context.Context, req *op.Request, sink op.Sink) (any, *op.Error)
	// Requests returns daemon HTTP round trips made so far — an in-process
	// dispatcher reports 0. Audits gate init round trips through it.
	Requests() int64
}

// Frontend is one MCP server bound to one dispatcher.
type Frontend struct {
	disp  Dispatcher
	srv   *mcp.Server
	opt   Options
	ops   map[string]string
	metas map[string]*op.Meta
}

// New builds an MCP server whose tools are derived entirely from the
// dispatcher's operation catalog: the advertised input schemas are exactly
// the canonical ones (plus the transport-level project_code), and every call
// is validated by the daemon against the same schemas.
func New(ctx context.Context, disp Dispatcher, opt Options) (*Frontend, error) {
	proto, ver, metas, err := disp.Catalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	if proto != schemas.Protocol || ver != schemas.Version {
		return nil, fmt.Errorf("dispatcher speaks %s v%d, expected %s v%d",
			proto, ver, schemas.Protocol, schemas.Version)
	}
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "game-forge",
		Version: Version,
	}, nil)
	f := &Frontend{disp: disp, srv: srv, opt: opt, ops: map[string]string{}, metas: map[string]*op.Meta{}}
	for i := range metas {
		if err := f.add(metas[i].Op, &metas[i]); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// Server returns the underlying MCP server (for streamable-HTTP mounting).
func (f *Frontend) Server() *mcp.Server { return f.srv }

// HTTPHandler mounts the MCP server as a Streamable HTTP handler — the
// daemon's canonical /mcp endpoint. The daemon injects it at startup so the
// transport lives inside its one listener and MCP calls share the daemon's
// in-process dispatcher (no HTTP loop into itself).
func HTTPHandler(disp Dispatcher, opt Options) (http.Handler, error) {
	fe, err := New(context.Background(), disp, opt)
	if err != nil {
		return nil, err
	}
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return fe.Server()
	}, nil), nil
}

// Run serves MCP over the given transport until it closes.
func (f *Frontend) Run(ctx context.Context, t mcp.Transport) error {
	return f.srv.Run(ctx, t)
}

// OpName reports the canonical operation behind a tool name, for diagnostics.
func (f *Frontend) OpName(tool string) string { return f.ops[tool] }

// toolName maps a canonical op name to an MCP tool name. Dots are not
// allowed in tool names; the mapping is deterministic and collisions are a
// build error, so a future "scenario_run"-style op name cannot shadow
// "scenario.run".
func toolName(opName string) string {
	return strings.ReplaceAll(opName, ".", "_")
}

// add registers one operation as an MCP tool. The tool's input schema is the
// compact projection of the canonical input schema plus project_code, and
// optionally the canonical output schema.
func (f *Frontend) add(opName string, meta *op.Meta) error {
	name := toolName(opName)
	if _, dup := f.ops[name]; dup {
		return fmt.Errorf("tool name collision for operation %q", opName)
	}
	desc := meta.Summary
	if desc == "" {
		desc = opName
	}
	in := injectProjectCode(compactSchema(meta.Input))
	t := &mcp.Tool{
		Name:        name,
		Description: desc,
		InputSchema: in,
	}
	if f.opt.FullSchemas {
		t.OutputSchema = compactSchema(meta.Output)
	}
	f.srv.AddTool(t, f.call)
	f.ops[name] = opName
	f.metas[opName] = meta
	return nil
}

// projectCodeSchema is the MCP-only transport property injected into every
// tool. Compact by contract: the registry validates the code semantically, so
// the wire only needs the type.
var projectCodeSchema = map[string]any{"type": "string"}

// injectProjectCode adds the required transport-level project_code property
// to a compact input schema. Canonical operation schemas stay untouched —
// project selection is request context, not operation semantics.
func injectProjectCode(in json.RawMessage) json.RawMessage {
	var doc map[string]any
	if err := json.Unmarshal(in, &doc); err != nil || doc == nil {
		return in
	}
	props, _ := doc["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
		doc["properties"] = props
	}
	props["project_code"] = projectCodeSchema
	req, _ := doc["required"].([]any)
	seen := false
	for _, r := range req {
		if r == "project_code" {
			seen = true
		}
	}
	if !seen {
		doc["required"] = append(req, "project_code")
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return in
	}
	return out
}

// progressSink forwards semantic progress as MCP progress notifications. Only
// stage/artifact transitions are forwarded — raw logs never leave the daemon.
// Consecutive identical messages are suppressed and messages are capped, so
// progress carries semantic state changes only.
type progressSink struct {
	send func(msg string)
	last atomic.Value
}

// Stage maps a stage transition to one compact progress message.
func (p *progressSink) Stage(name, status string, _ int64, detail string) {
	msg := name + " " + status
	if detail != "" {
		msg += " " + detail
	}
	p.send(msg)
}

// Artifact maps an artifact event to a compact reference, never its bytes.
func (p *progressSink) Artifact(kind, ref, _ string) {
	p.send(kind + " " + ref)
}

func (p *progressSink) emit(ctx context.Context, req *mcp.CallToolRequest, msg string) {
	if len(msg) > maxProgressText {
		msg = msg[:maxProgressText]
	}
	if prev, ok := p.last.Load().(string); ok && prev == msg {
		return // repeated identical progress adds no information
	}
	p.last.Store(msg)
	_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{Message: msg})
}

// call executes one tool call. It extracts the transport-level project_code,
// forwards canonical args to the dispatcher, and returns structuredContent —
// the canonical result JSON — with no duplicated text by default.
func (f *Frontend) call(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	opName, ok := f.ops[req.Params.Name]
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", req.Params.Name)
	}
	var args map[string]json.RawMessage
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return f.errResult(&op.Error{Code: op.CodeInvalidArgs, Path: "/", Msg: "arguments are not valid JSON"}), nil
		}
	}
	code, err := takeProjectCode(args)
	if err != nil {
		return f.errResult(err), nil
	}
	canonical, _ := json.Marshal(args)
	p := &progressSink{}
	p.send = func(msg string) { p.emit(ctx, req, msg) }
	data, werr := f.disp.Run(ctx, &op.Request{
		V: schemas.Version, Op: opName, Args: canonical, Project: code,
	}, p)
	if werr != nil {
		return f.errResult(werr), nil
	}
	res := &mcp.CallToolResult{Content: []mcp.Content{}}
	if data != nil {
		raw, _ := json.Marshal(data)
		res.StructuredContent = json.RawMessage(raw)
		if f.opt.CompatText {
			res.Content = []mcp.Content{&mcp.TextContent{Text: string(raw)}}
		}
	}
	return res, nil
}

// takeProjectCode removes project_code from args and returns it. It is
// required on every tool: explicit per-call routing is what lets concurrent
// calls for different projects share one MCP service safely.
func takeProjectCode(args map[string]json.RawMessage) (string, *op.Error) {
	raw, ok := args["project_code"]
	if !ok {
		return "", &op.Error{Code: op.CodeInvalidArgs, Path: "/project_code", Msg: "project_code is required"}
	}
	delete(args, "project_code")
	var code string
	if err := json.Unmarshal(raw, &code); err != nil || code == "" {
		return "", &op.Error{Code: op.CodeInvalidArgs, Path: "/project_code", Msg: "project_code must be a non-empty string"}
	}
	return code, nil
}

// errText renders a canonical wire error as compact MCP text: stable code,
// concise message, argument path — hard-capped and free of internals.
func errText(e *op.Error) string {
	text := e.Code + ": " + e.Msg
	if e.Path != "" {
		text += " (" + e.Path + ")"
	}
	if len(text) > maxErrorText {
		text = text[:maxErrorText]
	}
	return text
}

// errResult renders a canonical wire error as an MCP error result.
func (f *Frontend) errResult(e *op.Error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: errText(e)}},
	}
}

// compactSchema projects a canonical schema onto the MCP wire: the document
// minus $schema/$id metadata, which is authoring/reference metadata the model
// never needs and which carries no validation semantics. Documents containing
// $ref are returned untouched — $id participates in reference resolution, so
// stripping it could break a schema.
func compactSchema(in json.RawMessage) json.RawMessage {
	var doc map[string]any
	if err := json.Unmarshal(in, &doc); err != nil || doc == nil {
		return in
	}
	if hasRef(doc) {
		return in
	}
	stripMeta(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		return in
	}
	return out
}

// hasRef reports whether the schema tree uses $ref anywhere.
func hasRef(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, sub := range t {
			if k == "$ref" {
				return true
			}
			if hasRef(sub) {
				return true
			}
		}
	case []any:
		for _, sub := range t {
			if hasRef(sub) {
				return true
			}
		}
	}
	return false
}

// stripMeta removes $schema/$id keys recursively.
func stripMeta(v any) {
	switch t := v.(type) {
	case map[string]any:
		delete(t, "$schema")
		delete(t, "$id")
		for _, sub := range t {
			stripMeta(sub)
		}
	case []any:
		for _, sub := range t {
			stripMeta(sub)
		}
	}
}
