package mcpfrontend

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rceman/game-forge/internal/client"
	"github.com/rceman/game-forge/internal/core"
	"github.com/rceman/game-forge/internal/daemon"
	"github.com/rceman/game-forge/internal/op"
)

// stubDaemon stands up a real daemon HTTP handler over a stub registry and
// returns a client pointed at it. The MCP frontend exercises the same wire it
// uses against the real game-forged.
func stubDaemon(t *testing.T, extra ...*op.Operation) (*client.Client, func() int64) {
	t.Helper()
	reg := op.NewRegistry()

	obj := []byte(`{"type":"object","$id":"x/in"}`)
	out := []byte(`{"type":"object","$id":"x/out"}`)

	// cancelMarks counts how many times the slow op's ctx was canceled.
	var cancelCount int64
	var mu sync.Mutex
	slow := &op.Operation{
		Name:    "test.slow",
		Summary: "Blocks until canceled",
		Stream:  true,
		Handler: func(ctx context.Context, _ json.RawMessage, _ op.Sink) (any, error) {
			select {
			case <-ctx.Done():
				mu.Lock()
				cancelCount++
				mu.Unlock()
				return nil, ctx.Err()
			case <-time.After(30 * time.Second):
				return map[string]any{"ok": true}, nil
			}
		},
	}
	ops := append([]*op.Operation{
		{Name: "test.echo", Summary: "Echoes args", Handler: func(ctx context.Context, args json.RawMessage, _ op.Sink) (any, error) {
			var v any
			_ = json.Unmarshal(args, &v)
			return map[string]any{"echo": v, "cwd": op.CwdFrom(ctx)}, nil
		}},
		{Name: "test.fail", Summary: "Always fails", Handler: func(context.Context, json.RawMessage, op.Sink) (any, error) {
			return nil, &op.Error{Code: op.CodeFailed, Msg: "nope"}
		}},
		{Name: "test.strict", Summary: "Requires x", Handler: func(context.Context, json.RawMessage, op.Sink) (any, error) {
			return map[string]any{"ok": true}, nil
		}},
		{Name: "test.progress", Summary: "Emits stages", Stream: true, Handler: func(_ context.Context, _ json.RawMessage, sink op.Sink) (any, error) {
			sink.Stage("build", op.StatusPass, 3, "")
			sink.Stage("test", op.StatusPass, 5, "2 cases")
			return map[string]any{"ok": true}, nil
		}},
		slow,
	}, extra...)

	for _, o := range ops {
		in := []byte(strings.ReplaceAll(string(obj), `"$id":"x/in"`, `"$id":"`+o.Name+`/in"`))
		outS := []byte(strings.ReplaceAll(string(out), `"$id":"x/out"`, `"$id":"`+o.Name+`/out"`))
		if o.Name == "test.strict" {
			in = []byte(`{"type":"object","$id":"test.strict/in","required":["x"],"properties":{"x":{"type":"integer"}},"additionalProperties":false}`)
		}
		if err := reg.AddRaw(o, in, outS); err != nil {
			t.Fatal(err)
		}
	}

	ds := daemon.NewServer(core.NewRuntime(false), reg, nil)
	httpSrv := httptest.NewServer(ds.Handler())
	t.Cleanup(httpSrv.Close)
	cl := client.NewClient(&daemon.Discovery{
		Protocol: daemon.Protocol, Endpoint: httpSrv.URL, PID: 1, Token: ds.Token(),
	}, t.TempDir())
	return cl, func() int64 { mu.Lock(); defer mu.Unlock(); return cancelCount }
}

// mcpClient connects an in-process MCP client to a fresh frontend bound to cwd.
func mcpClient(t *testing.T, cl *client.Client, cwd string) *mcp.ClientSession {
	t.Helper()
	fe, err := New(context.Background(), cl, cwd)
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	go fe.Run(ctx, st)
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestToolsDeriveFromRegistry(t *testing.T) {
	cl, _ := stubDaemon(t)
	cs := mcpClient(t, cl, "/proj/a")
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// One tool per registered operation, deterministic underscore names.
	want := map[string]bool{
		"test_echo": true, "test_fail": true, "test_strict": true,
		"test_progress": true, "test_slow": true,
	}
	if len(res.Tools) != len(want) {
		t.Fatalf("tool count %d != op count %d", len(res.Tools), len(want))
	}
	seen := map[string]bool{}
	for _, tool := range res.Tools {
		if seen[tool.Name] {
			t.Fatalf("duplicate tool name %q", tool.Name)
		}
		seen[tool.Name] = true
		if !want[tool.Name] {
			t.Fatalf("unexpected tool %q", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("tool %s has no description", tool.Name)
		}
	}
}

func TestToolSchemaIsCanonical(t *testing.T) {
	cl, _ := stubDaemon(t)
	fe, err := New(context.Background(), cl, "/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	// The frontend's recorded metadata IS the canonical schema document.
	meta := fe.metas["test.strict"]
	var in map[string]any
	json.Unmarshal(meta.Input, &in)
	if in["required"] == nil {
		t.Fatal("canonical input schema lost 'required'")
	}
	cs := mcpClient(t, cl, "/proj/a")
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var strict *mcp.Tool
	for _, tool := range res.Tools {
		if tool.Name == "test_strict" {
			strict = tool
		}
	}
	if strict == nil {
		t.Fatal("test_strict tool missing")
	}
	// The exposed input schema must equal the deterministic compact
	// projection of the canonical operation schema — validation-relevant
	// semantics preserved, wire metadata ($schema/$id) removed.
	got, _ := json.Marshal(strict.InputSchema)
	var want map[string]any
	json.Unmarshal(compactSchema(meta.Input), &want)
	var gotM map[string]any
	json.Unmarshal(got, &gotM)
	if !jsonEqual(want, gotM) {
		t.Fatalf("input schema drift:\n canon=%v\n mcp  =%v", want, gotM)
	}
}

func jsonEqual(a, b map[string]any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func TestCallEchoStructured(t *testing.T) {
	cl, _ := stubDaemon(t)
	cs := mcpClient(t, cl, "/proj/a")
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "test_echo",
		Arguments: map[string]any{"hello": "world"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("echo failed: %+v", res.Content)
	}
	sc, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(sc), `"hello":"world"`) {
		t.Fatalf("structured result missing echo: %s", sc)
	}
	if !strings.Contains(string(sc), `"cwd":"/proj/a"`) {
		t.Fatalf("cwd not forwarded: %s", sc)
	}
}

func TestCallErrorIsStructured(t *testing.T) {
	cl, _ := stubDaemon(t)
	cs := mcpClient(t, cl, "/proj/a")
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "test_fail", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected IsError result")
	}
	if len(res.Content) == 0 {
		t.Fatal("no error content")
	}
	txt := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(txt, "failed") || !strings.Contains(txt, "nope") {
		t.Fatalf("error lost code/message: %q", txt)
	}
}

func TestCallInvalidArgs(t *testing.T) {
	cl, _ := stubDaemon(t)
	cs := mcpClient(t, cl, "/proj/a")
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "test_strict", Arguments: map[string]any{"x": "not-an-int"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for invalid args")
	}
	txt := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(txt, "invalid_args") {
		t.Fatalf("expected invalid_args, got %q", txt)
	}
}

func TestCallUnknownTool(t *testing.T) {
	cl, _ := stubDaemon(t)
	cs := mcpClient(t, cl, "/proj/a")
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "nope", Arguments: map[string]any{}})
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestCwdForwardedPerFrontend(t *testing.T) {
	cl, _ := stubDaemon(t)
	csA := mcpClient(t, cl, "/proj/a")
	csB := mcpClient(t, cl, "/proj/b")
	for _, tc := range []struct {
		cs  *mcp.ClientSession
		cwd string
	}{{csA, "/proj/a"}, {csB, "/proj/b"}} {
		res, err := tc.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "test_echo", Arguments: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		sc, _ := json.Marshal(res.StructuredContent)
		if !strings.Contains(string(sc), `"cwd":"`+tc.cwd+`"`) {
			t.Fatalf("frontend did not forward cwd %q: %s", tc.cwd, sc)
		}
	}
}

func TestCancellationPropagates(t *testing.T) {
	cl, cancels := stubDaemon(t)
	cs := mcpClient(t, cl, "/proj/a")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "test_slow", Arguments: map[string]any{}})
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled call did not return")
	}
	deadline := time.Now().Add(3 * time.Second)
	for cancels() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if cancels() == 0 {
		t.Fatal("daemon-side operation was not canceled")
	}
}

func TestProgressNotification(t *testing.T) {
	cl, _ := stubDaemon(t)
	fe, err := New(context.Background(), cl, "/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	go fe.Run(context.Background(), st)
	var msgs []string
	var mu sync.Mutex
	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			mu.Lock()
			msgs = append(msgs, r.Params.Message)
			mu.Unlock()
		},
	})
	cs, err := c.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "test_progress",
		Arguments: map[string]any{},
		Meta:      mcp.Meta{"progressToken": "ptok-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal("progress op failed")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(msgs) < 2 {
		t.Fatalf("expected stage progress notifications, got %v", msgs)
	}
	joined := strings.Join(msgs, " | ")
	if !strings.Contains(joined, "build") || !strings.Contains(joined, "test") {
		t.Fatalf("progress messages lack stage names: %v", msgs)
	}
}
