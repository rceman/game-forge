package mcpfrontend

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rceman/game-forge/internal/core"
	"github.com/rceman/game-forge/internal/daemon"
	"github.com/rceman/game-forge/internal/op"
	"github.com/rceman/game-forge/internal/project"
)

// stubDaemon stands up a real daemon (the in-process dispatcher the canonical
// /mcp endpoint uses) over a stub registry. The MCP frontend exercises the
// same dispatch path as the streamable-HTTP endpoint.
func stubDaemon(t *testing.T, extra ...*op.Operation) (*daemon.Server, func() int64) {
	t.Helper()
	// Isolate durable state (project registry, MCP token) before the daemon
	// opens it.
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
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

	return daemon.NewServer(core.NewRuntime(false), reg, nil), func() int64 {
		mu.Lock()
		defer mu.Unlock()
		return cancelCount
	}
}

// registerProject creates a minimal valid project in a temp dir and registers
// it under code, returning the canonical root.
func registerProject(t *testing.T, code, projectID string) string {
	t.Helper()
	dir := t.TempDir()
	manifest := "contract: game-forge/v1\nproject:\n  id: " + projectID + "\n"
	if err := os.WriteFile(filepath.Join(dir, "game-forge.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := project.OpenRegistry()
	if err != nil {
		t.Fatal(err)
	}
	rec, err := reg.Add(code, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return rec.Root
}

// mcpClient connects an in-process MCP client to a fresh frontend bound to
// the daemon dispatcher — the same wiring the /mcp endpoint uses.
func mcpClient(t *testing.T, ds *daemon.Server) *mcp.ClientSession {
	t.Helper()
	fe, err := New(context.Background(), ds, Options{})
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
	ds, _ := stubDaemon(t)
	cs := mcpClient(t, ds)
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
	ds, _ := stubDaemon(t)
	fe, err := New(context.Background(), ds, Options{})
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
	cs := mcpClient(t, ds)
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
	// The exposed input schema equals the deterministic compact projection
	// of the canonical schema plus the injected transport-level
	// project_code — validation semantics preserved, wire metadata removed.
	got, _ := json.Marshal(strict.InputSchema)
	var want map[string]any
	json.Unmarshal(injectProjectCode(compactSchema(meta.Input)), &want)
	var gotM map[string]any
	json.Unmarshal(got, &gotM)
	if !jsonEqual(want, gotM) {
		t.Fatalf("input schema drift:\n canon=%v\n mcp  =%v", want, gotM)
	}
	// project_code is a required transport property on every tool.
	props, _ := gotM["properties"].(map[string]any)
	if props["project_code"] == nil {
		t.Fatal("advertised schema lacks project_code")
	}
	reqd, _ := gotM["required"].([]any)
	found := false
	for _, r := range reqd {
		if r == "project_code" {
			found = true
		}
	}
	if !found {
		t.Fatal("project_code not marked required")
	}
	// The canonical schema itself is untouched by the projection.
	if in["$id"] == nil {
		t.Fatal("canonical schema metadata was mutated")
	}
	for _, r := range in["required"].([]any) {
		if r == "project_code" {
			t.Fatal("project_code leaked into the canonical schema")
		}
	}
}

func jsonEqual(a, b map[string]any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func TestCallEchoStructured(t *testing.T) {
	ds, _ := stubDaemon(t)
	root := registerProject(t, "TEST", "test-proj")
	cs := mcpClient(t, ds)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "test_echo",
		Arguments: map[string]any{"project_code": "TEST", "hello": "world"},
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
	if !strings.Contains(string(sc), `"cwd":"`+root+`"`) {
		t.Fatalf("project_code did not resolve to registered root: %s", sc)
	}
	// project_code is stripped before canonical args reach the op.
	if strings.Contains(string(sc), "project_code") {
		t.Fatalf("project_code leaked into canonical args: %s", sc)
	}
}

func TestProjectCodeRequired(t *testing.T) {
	ds, _ := stubDaemon(t)
	registerProject(t, "TEST", "test-proj")
	cs := mcpClient(t, ds)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "test_echo", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected IsError when project_code is missing")
	}
	txt := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(txt, "invalid_args") || !strings.Contains(txt, "project_code") {
		t.Fatalf("expected invalid_args project_code error, got %q", txt)
	}
}

func TestUnknownProjectCode(t *testing.T) {
	ds, _ := stubDaemon(t)
	cs := mcpClient(t, ds)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "test_echo", Arguments: map[string]any{"project_code": "NOPE"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for unregistered code")
	}
	txt := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(txt, "unknown_project") || !strings.Contains(txt, "NOPE") {
		t.Fatalf("expected unknown_project error, got %q", txt)
	}
}

func TestCallErrorIsStructured(t *testing.T) {
	ds, _ := stubDaemon(t)
	registerProject(t, "TEST", "test-proj")
	cs := mcpClient(t, ds)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "test_fail", Arguments: map[string]any{"project_code": "TEST"}})
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
	ds, _ := stubDaemon(t)
	registerProject(t, "TEST", "test-proj")
	cs := mcpClient(t, ds)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "test_strict", Arguments: map[string]any{"project_code": "TEST", "x": "not-an-int"},
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
	ds, _ := stubDaemon(t)
	cs := mcpClient(t, ds)
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "nope", Arguments: map[string]any{}})
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

// TestProjectRoutingPerCall is the multi-project isolation proof: two
// registered codes through ONE frontend/dispatcher resolve to their own
// canonical roots concurrently — no mutable current-project state exists.
func TestProjectRoutingPerCall(t *testing.T) {
	ds, _ := stubDaemon(t)
	rootA := registerProject(t, "AAA", "proj-a")
	rootB := registerProject(t, "BBB", "proj-b")
	cs := mcpClient(t, ds)

	var wg sync.WaitGroup
	errs := make(chan string, 20)
	call := func(code, want string) {
		defer wg.Done()
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "test_echo", Arguments: map[string]any{"project_code": code}})
		if err != nil {
			errs <- err.Error()
			return
		}
		sc, _ := json.Marshal(res.StructuredContent)
		if !strings.Contains(string(sc), `"cwd":"`+want+`"`) {
			errs <- "code " + code + " resolved to wrong root: " + string(sc)
		}
	}
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go call("AAA", rootA)
		go call("BBB", rootB)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
}

// TestProjectRegistryLiveUpdate proves the running daemon sees project CRUD
// without restart: add resolves immediately, remove stops resolving.
func TestProjectRegistryLiveUpdate(t *testing.T) {
	ds, _ := stubDaemon(t)
	root := registerProject(t, "LIVE", "proj-live")
	cs := mcpClient(t, ds)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "test_echo", Arguments: map[string]any{"project_code": "LIVE"}})
	if err != nil || res.IsError {
		t.Fatalf("call failed: %v %+v", err, res)
	}
	sc, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(sc), `"cwd":"`+root+`"`) {
		t.Fatalf("wrong root: %s", sc)
	}
	reg, _ := project.OpenRegistry()
	if err := reg.Remove("LIVE"); err != nil {
		t.Fatal(err)
	}
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "test_echo", Arguments: map[string]any{"project_code": "LIVE"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "unknown_project") {
		t.Fatalf("expected unknown_project after removal: %+v", res.Content)
	}
}

func TestCancellationPropagates(t *testing.T) {
	ds, cancels := stubDaemon(t)
	registerProject(t, "TEST", "test-proj")
	cs := mcpClient(t, ds)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "test_slow", Arguments: map[string]any{"project_code": "TEST"}})
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
	ds, _ := stubDaemon(t)
	registerProject(t, "TEST", "test-proj")
	fe, err := New(context.Background(), ds, Options{})
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
		Arguments: map[string]any{"project_code": "TEST"},
		Meta:      mcp.Meta{"progressToken": "ptok-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal("progress op failed")
	}
	// Notifications ride the client's async read loop; give it a moment to
	// drain after the synchronous in-process dispatch returns.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(msgs)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
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
