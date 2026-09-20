package mcpfrontend

// Streamable-HTTP tests: the canonical /mcp transport end to end — daemon
// auth gate, durable credential, project_code routing — through the official
// SDK's HTTP client transport, not the in-memory one.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rceman/game-forge/internal/daemon"
)

// bearerTransport injects the durable MCP credential on every request — the
// shape a configured MCP client uses.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

// mcpHTTPServer mounts the real /mcp route behind the daemon's auth on a
// httptest listener and returns its URL + the daemon server.
func mcpHTTPServer(t *testing.T) (*httptest.Server, *daemon.Server) {
	t.Helper()
	ds, _ := stubDaemon(t)
	h, err := HTTPHandler(ds, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ds.SetMCP(h)
	srv := httptest.NewServer(ds.Handler())
	t.Cleanup(srv.Close)
	return srv, ds
}

func mcpHTTPConnect(t *testing.T, url, token string) *mcp.ClientSession {
	t.Helper()
	tr := &mcp.StreamableClientTransport{
		Endpoint:   url + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{token: token, base: http.DefaultTransport}},
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "http-test", Version: "0"}, nil)
	cs, err := c.Connect(context.Background(), tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestStreamableHTTPEndToEnd(t *testing.T) {
	srv, ds := mcpHTTPServer(t)
	root := registerProject(t, "HTTP", "proj-http")

	// Wrong credentials → the daemon auth gate rejects before MCP begins.
	tr := &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{token: "wrong", base: http.DefaultTransport}},
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "bad", Version: "0"}, nil)
	if _, err := c.Connect(context.Background(), tr, nil); err == nil {
		t.Fatal("wrong MCP credential must not reach MCP")
	}

	cs := mcpHTTPConnect(t, srv.URL, ds.MCPToken())
	tl, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Tools) == 0 {
		t.Fatal("no tools over streamable HTTP")
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "test_echo",
		Arguments: map[string]any{"project_code": "HTTP", "via": "http"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("call failed over HTTP: %+v", res.Content)
	}
	sc, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(sc), `"via":"http"`) || !strings.Contains(string(sc), `"cwd":"`+root+`"`) {
		t.Fatalf("project_code routing wrong over HTTP: %s", sc)
	}
}

// TestStreamableHTTPMultiProject is the acceptance-level proof of the whole
// architecture: ONE MCP endpoint serving interleaved calls for two registered
// projects with no session state and no per-project process.
func TestStreamableHTTPMultiProject(t *testing.T) {
	srv, ds := mcpHTTPServer(t)
	rootA := registerProject(t, "AAA", "proj-a")
	rootB := registerProject(t, "BBB", "proj-b")
	cs := mcpHTTPConnect(t, srv.URL, ds.MCPToken())

	for code, want := range map[string]string{"AAA": rootA, "BBB": rootB} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "test_echo", Arguments: map[string]any{"project_code": code}})
		if err != nil {
			t.Fatal(err)
		}
		sc, _ := json.Marshal(res.StructuredContent)
		if !strings.Contains(string(sc), `"cwd":"`+want+`"`) {
			t.Fatalf("%s routed wrong: %s", code, sc)
		}
	}
	// And an unknown code is a bounded error result, not a transport failure.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "test_echo", Arguments: map[string]any{"project_code": "NOPE"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "unknown_project") {
		t.Fatalf("expected unknown_project error result: %+v", res.Content)
	}
}
