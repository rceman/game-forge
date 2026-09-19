// Package client is the in-process daemon client used by the CLI.
//
// It reads the ephemeral discovery state, verifies liveness with an
// authenticated health check, and transparently starts the daemon when none is
// running. Ordinary commands therefore never require a manual daemon start.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rceman/game-forge/internal/daemon"
	"github.com/rceman/game-forge/internal/op"
	"github.com/rceman/game-forge/schemas"
)

// Client talks to one running daemon.
type Client struct {
	d  *daemon.Discovery
	hc *http.Client
	// streamHC has no timeout: a streamed operation manages its own deadline,
	// and a fixed client timeout would cut a long run short.
	streamHC *http.Client
	// cwd is the caller's project context, sent in every request envelope so
	// the daemon discovers the right project. Defaults to the process cwd; a
	// frontend (the MCP server) may pin it via WithCwd.
	cwd string
}

// NewClient returns a client for a known daemon. It is used by tests and by
// frontends that already hold discovery state; Connect/Ensure are the normal
// entry points.
func NewClient(d *daemon.Discovery, cwd string) *Client {
	return &Client{d: d, hc: &http.Client{Timeout: 30 * time.Second}, streamHC: &http.Client{}, cwd: cwd}
}

// WithCwd returns a client identical to c but pinning the request cwd.
func (c *Client) WithCwd(cwd string) *Client {
	n := *c
	n.cwd = cwd
	return &n
}

// Endpoint returns the daemon base URL.
func (c *Client) Endpoint() string { return c.d.Endpoint }

// Discovery returns the daemon discovery record.
func (c *Client) Discovery() *daemon.Discovery { return c.d }

// Connect returns a client for a running daemon, or nil when none is healthy.
func Connect(ctx context.Context) *Client {
	d, err := daemon.Read()
	if err != nil || d == nil {
		return nil
	}
	c := NewClient(d, mustGetwd())
	if !c.healthy(ctx) {
		return nil
	}
	return c
}

// mustGetwd returns the process cwd, or "" when it cannot be determined.
func mustGetwd() string {
	wd, _ := os.Getwd()
	return wd
}

// healthy reports whether the daemon answers an authenticated health check.
func (c *Client) healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.d.Endpoint+"/health", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+c.d.Token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var v struct {
		OK bool `json:"ok"`
	}
	return json.NewDecoder(resp.Body).Decode(&v) == nil && v.OK
}

// Run executes one operation and returns its data or the structured error.
func (c *Client) Run(ctx context.Context, opName string, args any) (json.RawMessage, *op.Error) {
	reqBody, err := c.marshalRequest("", opName, args)
	if err != nil {
		return nil, &op.Error{Code: op.CodeInternal, Msg: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.d.Endpoint+"/v1/run", bytes.NewReader(reqBody))
	if err != nil {
		return nil, &op.Error{Code: op.CodeInternal, Msg: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+c.d.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, &op.Error{Code: op.CodeFailed, Msg: "daemon request: " + err.Error()}
	}
	defer resp.Body.Close()
	var reply op.Response
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, &op.Error{Code: op.CodeInternal, Msg: "decode reply: " + err.Error()}
	}
	if reply.Err != nil {
		return nil, reply.Err
	}
	raw, _ := json.Marshal(reply.Data)
	return raw, nil
}

// Stream executes one operation, invoking onEvent for each NDJSON event, and
// returns the final event's data.
func (c *Client) Stream(ctx context.Context, opName string, args any, onEvent func(op.Event)) (json.RawMessage, *op.Error) {
	reqBody, err := c.marshalRequest("", opName, args)
	if err != nil {
		return nil, &op.Error{Code: op.CodeInternal, Msg: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.d.Endpoint+"/v1/run", bytes.NewReader(reqBody))
	if err != nil {
		return nil, &op.Error{Code: op.CodeInternal, Msg: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+c.d.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson")
	resp, err := c.streamHC.Do(req)
	if err != nil {
		return nil, &op.Error{Code: op.CodeFailed, Msg: "daemon request: " + err.Error()}
	}
	defer resp.Body.Close()
	// A rejected request (bad args, unknown op, wrong version) answers with a
	// plain JSON response envelope, not an NDJSON stream — return its error.
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/x-ndjson") {
		var reply op.Response
		if derr := json.NewDecoder(resp.Body).Decode(&reply); derr != nil {
			return nil, &op.Error{Code: op.CodeInternal, Msg: "decode reply: " + derr.Error()}
		}
		if reply.Err != nil {
			return nil, reply.Err
		}
		raw, _ := json.Marshal(reply.Data)
		return raw, nil
	}
	dec := json.NewDecoder(resp.Body)
	var last *op.Event
	for {
		var ev op.Event
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, &op.Error{Code: op.CodeInternal, Msg: "decode event: " + err.Error()}
		}
		if onEvent != nil {
			onEvent(ev)
		}
		if ev.Ev == op.EvDone {
			e := ev
			last = &e
		}
	}
	if last == nil {
		return nil, &op.Error{Code: op.CodeInternal, Msg: "stream ended without a done event"}
	}
	if last.Err != nil {
		return nil, last.Err
	}
	raw, _ := json.Marshal(last.Data)
	return raw, nil
}

// Capabilities returns the operations the daemon exposes.
func (c *Client) Capabilities(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.d.Endpoint+"/v1/capabilities", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.d.Token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var v struct {
		Ops []string `json:"ops"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	return v.Ops, nil
}

// Shutdown asks the daemon to stop gracefully.
func (c *Client) Shutdown(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.d.Endpoint+"/v1/shutdown", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.d.Token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// OpSchema is the canonical contract document for one operation, as served by
// GET /v1/schema/<op>. The MCP frontend builds tools directly from these so
// the exposed schemas are exactly what the daemon validates and executes.
type OpSchema struct {
	Op      string          `json:"op"`
	Summary string          `json:"summary"`
	Stream  bool            `json:"stream"`
	Input   json.RawMessage `json:"input"`
	Output  json.RawMessage `json:"output"`
}

// Schema returns the canonical input/output contract for one operation.
func (c *Client) Schema(ctx context.Context, opName string) (*OpSchema, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.d.Endpoint+"/v1/schema/"+opName, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.d.Token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var v OpSchema
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	return &v, nil
}

// Health is the daemon's liveness/compatibility payload.
type Health struct {
	OK       bool   `json:"ok"`
	V        int    `json:"v"`
	Protocol string `json:"protocol"`
	PID      int    `json:"pid"`
}

// HealthCheck returns the daemon health document so a frontend can verify the
// connected daemon speaks the protocol this binary was built against.
func (c *Client) HealthCheck(ctx context.Context) (*Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.d.Endpoint+"/health", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.d.Token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var h Health
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return nil, err
	}
	return &h, nil
}

// marshalRequest builds a request envelope body, forwarding the client's
// pinned cwd so the daemon resolves the caller's project.
func (c *Client) marshalRequest(id, opName string, args any) ([]byte, error) {
	cwd := c.cwd
	if cwd == "" {
		cwd = mustGetwd()
	}
	var rawArgs json.RawMessage
	var err error
	if args != nil {
		rawArgs, err = json.Marshal(args)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(op.Request{V: schemas.Version, ID: id, Op: opName, Cwd: cwd, Args: rawArgs})
}

// Ensure returns a client for a running daemon, starting one if needed.
func Ensure(ctx context.Context) (*Client, error) {
	if c := Connect(ctx); c != nil {
		return c, nil
	}
	return start(ctx)
}
