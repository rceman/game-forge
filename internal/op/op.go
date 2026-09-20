// Package op defines Game Forge's Operation Registry: the single place where an
// externally callable operation's name, schemas and Core handler are declared.
//
// Every frontend is an adapter over this registry. The CLI parses its familiar
// syntax into an op.Request; the daemon decodes HTTP into the same op.Request;
// a future MCP frontend will map tool calls onto the same names. None of them
// may implement operation semantics of their own, and none may shell out to the
// CLI to reach the Core.
package op

import (
	"context"
	"encoding/json"
	"fmt"
)

// Error is the single structured error shape used by every operation, by the
// non-streaming reply envelope and by streamed terminal events.
type Error struct {
	Code string `json:"code"`
	Path string `json:"path,omitempty"`
	Msg  string `json:"msg"`
}

// Error implements the error interface so handlers may return *Error directly.
func (e *Error) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("%s at %s: %s", e.Code, e.Path, e.Msg)
	}
	return e.Code + ": " + e.Msg
}

// Error codes. These are part of the wire contract and are stable.
const (
	CodeInvalidRequest = "invalid_request"
	CodeUnsupported    = "unsupported_version"
	CodeUnknownOp      = "unknown_op"
	CodeInvalidArgs    = "invalid_args"
	CodeInvalidOutput  = "invalid_output"
	CodeCanceled       = "canceled"
	CodeFailed         = "failed"
	CodeInternal       = "internal"

	// Project-selector errors. A registered project code may be unknown, its
	// directory may have disappeared, or the directory may now contain a
	// different project than the one registered.
	CodeUnknownProject     = "unknown_project"
	CodeProjectUnavailable = "project_unavailable"
	CodeProjectChanged     = "project_changed"
)

// Request is the canonical control request envelope. It is deliberately terse
// but readable: "v", "id", "op", "cwd", "args".
type Request struct {
	// V is the protocol version. Always 1 for this build.
	V int `json:"v"`
	// ID correlates streamed events and the final reply. Optional.
	ID string `json:"id,omitempty"`
	// Op is the canonical operation name, e.g. "scenario.run".
	Op string `json:"op"`
	// Cwd is the client's working directory. The daemon has its own, so project
	// discovery must be told where the caller was.
	Cwd string `json:"cwd,omitempty"`
	// Project selects a registered project by its stable code (e.g. "TDG").
	// It is transport metadata: the daemon resolves it to the registered
	// canonical root before dispatch. Exactly one of Cwd or Project may be
	// set on a request that needs project context.
	Project string `json:"project,omitempty"`
	// Args is the operation's own argument object.
	Args json.RawMessage `json:"args,omitempty"`
}

// Response is the non-streaming reply envelope.
type Response struct {
	ID   string `json:"id,omitempty"`
	OK   bool   `json:"ok"`
	Data any    `json:"data,omitempty"`
	Err  *Error `json:"err,omitempty"`
}

// Event kinds emitted on an NDJSON stream.
const (
	EvStart    = "start"
	EvStage    = "stage"
	EvArtifact = "artifact"
	EvDone     = "done"
)

// Event is one line of an NDJSON stream. Fields are omitted when they do not
// apply, so unchanged metadata is never repeated per line.
type Event struct {
	ID     string `json:"id,omitempty"`
	Ev     string `json:"ev"`
	Run    string `json:"run,omitempty"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
	MS     int64  `json:"ms,omitempty"`
	Data   any    `json:"data,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Path   string `json:"path,omitempty"`
	Code   *int   `json:"code,omitempty"`
	Err    *Error `json:"err,omitempty"`
}

// Stage statuses.
const (
	StatusPass = "pass"
	StatusFail = "fail"
)

// Sink receives progress from a running operation. Handlers must treat it as
// optional and non-blocking.
type Sink interface {
	// Stage reports one named step, how long it took, and a short detail.
	Stage(name, status string, ms int64, detail string)
	// Artifact reports a large result by reference rather than by value.
	Artifact(kind, ref, path string)
}

// NopSink discards progress. It is used when a caller only wants the result.
type NopSink struct{}

// Stage implements Sink.
func (NopSink) Stage(string, string, int64, string) {}

// Artifact implements Sink.
func (NopSink) Artifact(string, string, string) {}

// Meta is one operation's canonical contract as advertised to frontends: the
// daemon's /v1/catalog response, the daemon's in-process MCP dispatcher and
// the daemon client's HTTP adapter all use this single shape so tool
// construction is driven by the registry, never a second copy.
type Meta struct {
	Op      string          `json:"op"`
	Summary string          `json:"summary"`
	Stream  bool            `json:"stream"`
	Input   json.RawMessage `json:"input"`
	Output  json.RawMessage `json:"output"`
}

// Handler is the single Core implementation of an operation.
//
// args is the raw argument object; the registry has already validated it
// against the operation's input schema, so the handler may decode it directly.
type Handler func(ctx context.Context, args json.RawMessage, sink Sink) (any, error)

// cwdKey carries the caller's working directory through a request context.
//
// The daemon has its own working directory, so project discovery must be told
// where the caller was. This is transport metadata, not operation semantics.
type cwdKey struct{}

// WithCwd records the caller's working directory on a request context.
func WithCwd(ctx context.Context, cwd string) context.Context {
	return context.WithValue(ctx, cwdKey{}, cwd)
}

// CwdFrom returns the caller's working directory, or "" when unset.
func CwdFrom(ctx context.Context) string {
	v, _ := ctx.Value(cwdKey{}).(string)
	return v
}

// runKey carries the daemon's run identity so a handler's Core can group the
// resources it creates under the same id the stream reports.
type runKey struct{}

// WithRunID records the daemon run id on a request context.
func WithRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runKey{}, runID)
}

// RunIDFrom returns the daemon run id, or "" when unset.
func RunIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(runKey{}).(string)
	return v
}
