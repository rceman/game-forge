package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rceman/game-forge/internal/core"
	"github.com/rceman/game-forge/internal/op"
)

const testToken = "test-secret"

// testServer builds a daemon HTTP handler over a stub registry.
func testServer(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	reg := op.NewRegistry()

	obj := []byte(`{"type":"object","$id":"t/in"}`)
	out := []byte(`{"type":"object","$id":"t/out","required":["ok"],"properties":{"ok":{"type":"boolean"}},"additionalProperties":false}`)

	echo := &op.Operation{
		Name: "test.echo",
		Handler: func(ctx context.Context, args json.RawMessage, sink op.Sink) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	}
	fail := &op.Operation{
		Name: "test.fail",
		Handler: func(ctx context.Context, args json.RawMessage, sink op.Sink) (any, error) {
			return nil, &op.Error{Code: op.CodeFailed, Msg: "nope"}
		},
	}
	stream := &op.Operation{
		Name:   "test.stream",
		Stream: true,
		Handler: func(ctx context.Context, args json.RawMessage, sink op.Sink) (any, error) {
			sink.Stage("one", op.StatusPass, 5, "")
			sink.Stage("two", op.StatusPass, 7, "")
			return map[string]any{"ok": true}, nil
		},
	}
	// strict arg schema: requires "x"
	strictIn := []byte(`{"type":"object","$id":"s/in","required":["x"],"properties":{"x":{"type":"integer"}},"additionalProperties":false}`)
	strict := &op.Operation{
		Name: "test.strict",
		Handler: func(ctx context.Context, args json.RawMessage, sink op.Sink) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	}
	// badout produces schema-invalid output.
	badout := &op.Operation{
		Name: "test.badout",
		Handler: func(ctx context.Context, args json.RawMessage, sink op.Sink) (any, error) {
			return map[string]any{"unexpected": true}, nil
		},
	}
	for _, o := range []*op.Operation{echo, fail, stream, strict, badout} {
		in := obj
		if o.Name == "test.strict" {
			in = strictIn
		}
		// give each op a distinct $id
		in = []byte(strings.Replace(string(in), `"$id":"t/in"`, `"$id":"`+o.Name+`/in"`, 1))
		in = []byte(strings.Replace(string(in), `"$id":"s/in"`, `"$id":"`+o.Name+`/in"`, 1))
		outS := []byte(strings.Replace(string(out), `"$id":"t/out"`, `"$id":"`+o.Name+`/out"`, 1))
		if err := reg.AddRaw(o, in, outS); err != nil {
			t.Fatalf("add %s: %v", o.Name, err)
		}
	}

	rt := core.NewRuntime(false)
	s := NewServer(rt, reg, nil)
	s.token = testToken
	return httptest.NewServer(s.routes()), s
}

func authed(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func TestAuthRequired(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	for _, path := range []string{"/health", "/v1/capabilities", "/v1/run"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without token: want 401, got %d", path, resp.StatusCode)
		}
	}
	// Wrong token is also rejected.
	req, _ := http.NewRequest("GET", srv.URL+"/health", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: want 401, got %d", resp.StatusCode)
	}
}

func TestHealth(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	resp, err := http.DefaultClient.Do(authed(t, "GET", srv.URL+"/health", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v map[string]any
	json.NewDecoder(resp.Body).Decode(&v)
	if v["ok"] != true || v["protocol"] != Protocol {
		t.Errorf("bad health: %v", v)
	}
}

func TestCapabilities(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	resp, err := http.DefaultClient.Do(authed(t, "GET", srv.URL+"/v1/capabilities", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v struct {
		Ops []string `json:"ops"`
	}
	json.NewDecoder(resp.Body).Decode(&v)
	found := false
	for _, n := range v.Ops {
		if n == "test.echo" {
			found = true
		}
	}
	if !found {
		t.Errorf("capabilities missing test.echo: %v", v.Ops)
	}
}

func TestSchemaEndpoint(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	resp, err := http.DefaultClient.Do(authed(t, "GET", srv.URL+"/v1/schema/test.echo", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v map[string]any
	json.NewDecoder(resp.Body).Decode(&v)
	if v["op"] != "test.echo" || v["input"] == nil || v["output"] == nil {
		t.Errorf("bad schema doc: %v", v)
	}
	// Unknown op → 404.
	resp2, err := http.DefaultClient.Do(authed(t, "GET", srv.URL+"/v1/schema/nope", ""))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("unknown op schema: want 404, got %d", resp2.StatusCode)
	}
}

func TestRunSimple(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	body := `{"v":1,"op":"test.echo","args":{}}`
	resp, err := http.DefaultClient.Do(authed(t, "POST", srv.URL+"/v1/run", body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v op.Response
	json.NewDecoder(resp.Body).Decode(&v)
	if !v.OK {
		t.Errorf("echo failed: %+v", v.Err)
	}
}

func TestRunFailure(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	body := `{"v":1,"op":"test.fail","args":{}}`
	resp, err := http.DefaultClient.Do(authed(t, "POST", srv.URL+"/v1/run", body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v op.Response
	json.NewDecoder(resp.Body).Decode(&v)
	if v.OK || v.Err == nil || v.Err.Code != op.CodeFailed {
		t.Errorf("expected structured failure, got %+v", v)
	}
}

func TestRunUnknownOp(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	body := `{"v":1,"op":"no.such","args":{}}`
	resp, err := http.DefaultClient.Do(authed(t, "POST", srv.URL+"/v1/run", body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown op: want 404, got %d", resp.StatusCode)
	}
}

func TestRunInvalidArgs(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	body := `{"v":1,"op":"test.strict","args":{"y":1}}`
	resp, err := http.DefaultClient.Do(authed(t, "POST", srv.URL+"/v1/run", body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v op.Response
	json.NewDecoder(resp.Body).Decode(&v)
	if v.Err == nil || v.Err.Code != op.CodeInvalidArgs {
		t.Errorf("expected invalid_args, got %+v", v.Err)
	}
}

func TestRunMalformedJSON(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	resp, err := http.DefaultClient.Do(authed(t, "POST", srv.URL+"/v1/run", "{not json"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed JSON: want 400, got %d", resp.StatusCode)
	}
}

func TestRunUnsupportedVersion(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	body := `{"v":99,"op":"test.echo","args":{}}`
	resp, err := http.DefaultClient.Do(authed(t, "POST", srv.URL+"/v1/run", body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v op.Response
	json.NewDecoder(resp.Body).Decode(&v)
	if v.Err == nil || v.Err.Code != op.CodeUnsupported {
		t.Errorf("expected unsupported_version, got %+v", v.Err)
	}
}

func TestRunOutputValidation(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	body := `{"v":1,"op":"test.badout","args":{}}`
	resp, err := http.DefaultClient.Do(authed(t, "POST", srv.URL+"/v1/run", body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v op.Response
	json.NewDecoder(resp.Body).Decode(&v)
	// A schema-invalid Core result is a contract bug → invalid_output, not sent.
	if v.Err == nil || v.Err.Code != op.CodeInvalidOutput {
		t.Errorf("expected invalid_output, got %+v", v.Err)
	}
}

func TestRunStreaming(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	body := `{"v":1,"id":"9","op":"test.stream","args":{}}`
	req := authed(t, "POST", srv.URL+"/v1/run", body)
	req.Header.Set("Accept", "application/x-ndjson")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("want ndjson content type, got %s", ct)
	}
	var evs []op.Event
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var ev op.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("bad event line %q: %v", sc.Text(), err)
		}
		evs = append(evs, ev)
	}
	// Expect: start, stage, stage, done.
	if len(evs) < 4 {
		t.Fatalf("expected >=4 events, got %d: %v", len(evs), evs)
	}
	if evs[0].Ev != op.EvStart {
		t.Errorf("first event should be start, got %s", evs[0].Ev)
	}
	if evs[1].Ev != op.EvStage || evs[2].Ev != op.EvStage {
		t.Errorf("expected two stage events, got %v %v", evs[1].Ev, evs[2].Ev)
	}
	last := evs[len(evs)-1]
	if last.Ev != op.EvDone || last.Code == nil || *last.Code != 0 {
		t.Errorf("expected done code=0, got %+v", last)
	}
}
