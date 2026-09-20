package daemon

// Endpoint + MCP-auth + project-resolution tests: the durable port, the
// durable MCP credential, and the registered-project selector on /v1/run.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rceman/game-forge/internal/op"
	"github.com/rceman/game-forge/internal/project"
)

// occupy binds a port and keeps it held until the test ends.
func occupy(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestPortPersistenceAndRebind(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	l, port, err := bindEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	if port < portMin || port > portMax {
		t.Fatalf("chosen port %d outside %d-%d", port, portMin, portMax)
	}
	l.Close()

	// Restart (new Server, same GAME_FORGE_HOME) must rebind the same port.
	l2, port2, err := bindEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if port2 != port {
		t.Fatalf("restart rebound port %d, want persisted %d", port2, port)
	}
	got, err := LoadEndpointPort()
	if err != nil || got != port {
		t.Fatalf("endpoint file wrong: %d %v", got, err)
	}
}

func TestPortConflictFails(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	held := occupy(t)
	port := held.Addr().(*net.TCPAddr).Port
	// Persist a port that is occupied — a restart onto it must fail loudly,
	// not silently pick another.
	if err := persistEndpointPort(port); err != nil {
		t.Fatal(err)
	}
	_, _, err := bindEndpoint()
	if err == nil {
		t.Fatal("bind on an occupied persisted port must fail")
	}
	if got := fmt.Sprint(err); !bytes.Contains([]byte(got), []byte(fmt.Sprint(port))) {
		t.Fatalf("conflict error should name the port: %v", err)
	}
}

func TestRebindPicksNewPort(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	l, port, err := bindEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := ClearEndpoint(); err != nil {
		t.Fatal(err)
	}
	l2, port2, err := bindEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	// After an explicit rebind the daemon may pick any free port — the first
	// is still held, so this must differ.
	if port2 == port {
		t.Fatal("rebind chose the same occupied port")
	}
}

// testServerBare builds a server without the HTTP harness — for bind tests.
func testServerBare(t *testing.T) *Server {
	t.Helper()
	reg := op.NewRegistry()
	return NewServer(nil, reg, nil)
}

func TestMCPAuth(t *testing.T) {
	_, s := testServer(t)
	s.SetMCP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	call := func(auth, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, req)
		return w
	}

	if w := call("", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no auth: got %d", w.Code)
	}
	if w := call("Bearer wrong", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d", w.Code)
	}
	// The ephemeral daemon token must NOT authenticate /mcp.
	if w := call("Bearer "+testToken, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("daemon token accepted on /mcp: got %d", w.Code)
	}
	if w := call("Bearer "+s.MCPToken(), ""); w.Code != http.StatusNoContent {
		t.Fatalf("durable MCP token rejected: got %d", w.Code)
	}
	// DNS-rebinding guard: a non-local Origin is refused.
	if w := call("Bearer "+s.MCPToken(), "http://evil.example.com"); w.Code != http.StatusForbidden {
		t.Fatalf("remote Origin allowed: got %d", w.Code)
	}
	if w := call("Bearer "+s.MCPToken(), "http://localhost:3000"); w.Code != http.StatusNoContent {
		t.Fatalf("local Origin rejected: got %d", w.Code)
	}
}

func TestMCPTokenNotOnV1(t *testing.T) {
	_, s := testServer(t)
	// The durable MCP credential must not open the control API.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Authorization", "Bearer "+s.MCPToken())
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("MCP token accepted on /health: %d", w.Code)
	}
}

func TestMCPTokenDurable(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	s1 := testServerBare(t)
	tok1 := s1.MCPToken()
	if len(tok1) < 32 {
		t.Fatalf("MCP token too short: %q", tok1)
	}
	// A new server in the same home loads the same credential.
	s2 := testServerBare(t)
	if s2.MCPToken() != tok1 {
		t.Fatal("MCP token not durable across daemon incarnations")
	}
	// 0600 on disk.
	info, err := os.Stat(filepath.Join(os.Getenv("GAME_FORGE_HOME"), "state", "mcp.token"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mcp-token perms %v", info.Mode().Perm())
	}
}

func TestRunProjectSelector(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	srv, _ := testServer(t)

	// Register a real project so "TEST" resolves.
	dir := t.TempDir()
	manifest := "contract: game-forge/v1\nproject:\n  id: tproj\n"
	if err := os.WriteFile(filepath.Join(dir, "game-forge.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := project.OpenRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Add("TEST", dir, false); err != nil {
		t.Fatal(err)
	}

	post := func(body string, tok string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/run", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		srv.Config.Handler.ServeHTTP(w, req)
		return w
	}

	// Unknown code → 400 unknown_project.
	w := post(`{"v":1,"op":"test.echo","project":"NOPE","args":{}}`, testToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown project: got %d", w.Code)
	}
	var resp op.Response
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Err == nil || resp.Err.Code != op.CodeUnknownProject {
		t.Fatalf("want unknown_project, got %+v", resp.Err)
	}

	// project + cwd together → invalid_request (envelope shape violation).
	w = post(`{"v":1,"op":"test.echo","project":"TEST","cwd":"/x","args":{}}`, testToken)
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Err == nil || resp.Err.Code != op.CodeInvalidRequest {
		t.Fatalf("project+cwd should be invalid_request, got %+v", resp.Err)
	}

	// Registered code resolves and runs.
	w = post(`{"v":1,"op":"test.echo","project":"TEST","args":{}}`, testToken)
	if w.Code != http.StatusOK {
		t.Fatalf("registered project call failed: %d %s", w.Code, w.Body)
	}
}
