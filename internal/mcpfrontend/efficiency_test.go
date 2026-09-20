package mcpfrontend

// Efficiency-contract tests: the MCP wire surface is gated on serialized
// bytes, dispatch calls and latency — not on source inspection. The catalog
// gates run against the REAL operation registry (canonical names, summaries
// and schemas); the call-level gates run through the official MCP SDK client
// against the daemon's in-process dispatcher — the same path /mcp serves.
//
// project_code is required on every call; tests register temp projects so the
// daemon resolves real registry entries.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rceman/game-forge/internal/core"
	"github.com/rceman/game-forge/internal/daemon"
	"github.com/rceman/game-forge/internal/op"
	"github.com/rceman/game-forge/schemas"
)

// countingDisp wraps a dispatcher and counts how it is used, so init/discovery
// gates can prove the frontend does O(1) catalog fetches and zero runs.
type countingDisp struct {
	d        Dispatcher
	catalogs atomic.Int64
	runs     atomic.Int64
}

func (c *countingDisp) Catalog(ctx context.Context) (string, int, []op.Meta, error) {
	c.catalogs.Add(1)
	return c.d.Catalog(ctx)
}

func (c *countingDisp) Run(ctx context.Context, req *op.Request, sink op.Sink) (any, *op.Error) {
	c.runs.Add(1)
	return c.d.Run(ctx, req, sink)
}

func (c *countingDisp) Requests() int64 { return c.d.Requests() }

// realDaemon serves the real operation registry (canonical schemas) through
// the daemon's in-process dispatcher — identical to what /mcp mounts.
// Handlers are the genuine Core implementations; catalog tests never invoke
// them.
func realDaemon(t *testing.T) (*daemon.Server, *countingDisp) {
	t.Helper()
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	reg := op.NewRegistry()
	if err := core.Register(reg, core.NewRuntime(false)); err != nil {
		t.Fatal(err)
	}
	ds := daemon.NewServer(core.NewRuntime(false), reg, nil)
	return ds, &countingDisp{d: ds}
}

// mcpClientOpt is mcpClient with explicit frontend options.
func mcpClientOpt(t *testing.T, disp Dispatcher, opt Options) *mcp.ClientSession {
	t.Helper()
	fe, err := New(context.Background(), disp, opt)
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

// serialized returns the byte length of a value's JSON encoding.
func serialized(t *testing.T, v any) int {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

// ---- catalog surface --------------------------------------------------------

func TestEfficiencyCatalogBudgets(t *testing.T) {
	_, disp := realDaemon(t)
	cs := mcpClientOpt(t, disp, Options{})
	b, err := LoadBudget()
	if err != nil {
		t.Fatal(err)
	}
	tl, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	catalog := serialized(t, tl)
	if catalog > b.MaxCatalogBytes {
		t.Errorf("tools/list %d B exceeds budget %d", catalog, b.MaxCatalogBytes)
	}
	var surface, total, maxTool int
	var maxName string
	for _, tool := range tl.Tools {
		surface += len(tool.Name) + len(tool.Description) + serialized(t, tool.InputSchema)
		if len(tool.Description) > b.MaxDescriptionBytesPerTool {
			t.Errorf("tool %s description %d B exceeds %d", tool.Name, len(tool.Description), b.MaxDescriptionBytesPerTool)
		}
		sz := serialized(t, tool)
		total += sz
		if sz > maxTool {
			maxTool, maxName = sz, tool.Name
		}
	}
	if surface > b.MaxModelSurfaceBytes {
		t.Errorf("model surface %d B exceeds budget %d", surface, b.MaxModelSurfaceBytes)
	}
	if maxTool > b.MaxToolBytes {
		t.Errorf("largest tool %s %d B exceeds budget %d", maxName, maxTool, b.MaxToolBytes)
	}
	if avg := total / len(tl.Tools); avg > b.MaxAvgToolBytes {
		t.Errorf("avg tool %d B exceeds budget %d", avg, b.MaxAvgToolBytes)
	}
	// Compact mode: no output schema is advertised on the wire.
	for _, tool := range tl.Tools {
		if tool.OutputSchema != nil {
			t.Errorf("tool %s exposes outputSchema in compact mode", tool.Name)
		}
	}
	// Every tool carries the required transport-level project_code.
	for _, tool := range tl.Tools {
		ib, _ := json.Marshal(tool.InputSchema)
		if !strings.Contains(string(ib), `"project_code"`) {
			t.Errorf("tool %s input schema lacks project_code", tool.Name)
		}
	}
	t.Logf("catalog=%dB surface=%dB tools=%d largest=%s %dB", catalog, surface, len(tl.Tools), maxName, maxTool)
}

// ---- initialization ---------------------------------------------------------

func TestEfficiencyInitRequests(t *testing.T) {
	_, disp := realDaemon(t)
	// Frontend init must be O(1): exactly one catalog fetch regardless of how
	// many operations exist, and zero operation runs.
	fe, err := New(context.Background(), disp, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = fe
	if disp.catalogs.Load() != 1 {
		t.Fatalf("init fetched the catalog %d times (want 1)", disp.catalogs.Load())
	}
	if disp.runs.Load() != 0 {
		t.Fatalf("init ran %d operations (want 0)", disp.runs.Load())
	}
	if n := disp.Requests(); n > 2 {
		t.Fatalf("init made %d daemon HTTP requests (want <= 2)", n)
	}
}

func TestEfficiencyDiscoveryIsMetadataOnly(t *testing.T) {
	_, disp := realDaemon(t)
	cs := mcpClientOpt(t, disp, Options{})
	if _, err := cs.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	// Init + tools/list may only touch the catalog — never dispatch an
	// operation, which is the only path that can start a browser, server or
	// GPU probe.
	if disp.runs.Load() != 0 {
		t.Fatalf("discovery invoked %d operations — it must be metadata-only", disp.runs.Load())
	}
}

// ---- call results -----------------------------------------------------------

func TestEfficiencyCompactResultNoDuplication(t *testing.T) {
	ds, _ := stubDaemon(t)
	registerProject(t, "TEST", "test-proj")
	cs := mcpClient(t, ds)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "test_echo", Arguments: map[string]any{"project_code": "TEST", "a": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("call failed: %+v", res.Content)
	}
	if res.StructuredContent == nil {
		t.Fatal("structured result missing")
	}
	// Compact mode: the standards-valid empty content array — the canonical
	// result is never serialized a second time into text.
	sb, _ := json.Marshal(res.StructuredContent)
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok && tc.Text == string(sb) {
			t.Fatalf("structured payload duplicated into text (%d B)", len(sb))
		}
	}
	var textBytes int
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			textBytes += len(tc.Text)
		}
	}
	if textBytes > 256 {
		t.Fatalf("success text fallback %d B exceeds 256", textBytes)
	}
}

func TestEfficiencyCompatTextMode(t *testing.T) {
	ds, _ := stubDaemon(t)
	registerProject(t, "TEST", "test-proj")
	cs := mcpClientOpt(t, ds, Options{CompatText: true})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "test_echo", Arguments: map[string]any{"project_code": "TEST", "a": 1}})
	if err != nil {
		t.Fatal(err)
	}
	// Compat mode deliberately mirrors the JSON for legacy clients.
	sb, _ := json.Marshal(res.StructuredContent)
	if len(res.Content) == 0 {
		t.Fatal("compat mode produced no text content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok || tc.Text != string(sb) {
		t.Fatalf("compat mode did not mirror structured JSON: %+v", res.Content)
	}
}

func TestEfficiencyFullSchemasMode(t *testing.T) {
	ds, _ := stubDaemon(t)
	cs := mcpClientOpt(t, ds, Options{FullSchemas: true})
	tl, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, tool := range tl.Tools {
		if tool.OutputSchema != nil {
			n++
			// Output schemas are the same compact projection as inputs.
			ob, _ := json.Marshal(tool.OutputSchema)
			if strings.Contains(string(ob), `"$id"`) || strings.Contains(string(ob), `"$schema"`) {
				t.Errorf("tool %s output schema carries wire metadata", tool.Name)
			}
		}
	}
	if n == 0 {
		t.Fatal("full-schemas mode exposed no output schemas")
	}
}

// ---- errors -----------------------------------------------------------------

func TestEfficiencyErrorBudget(t *testing.T) {
	ds, _ := stubDaemon(t)
	registerProject(t, "TEST", "test-proj")
	cs := mcpClient(t, ds)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "test_strict", Arguments: map[string]any{"project_code": "TEST", "x": "bad"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected error result")
	}
	var textBytes int
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			textBytes += len(tc.Text)
			if strings.Contains(tc.Text, "Bearer") || strings.Contains(tc.Text, "/v1/") {
				t.Fatalf("error leaks internals: %q", tc.Text)
			}
		}
	}
	b, _ := LoadBudget()
	if textBytes > b.MaxErrorTextBytes {
		t.Fatalf("error text %d B exceeds %d", textBytes, b.MaxErrorTextBytes)
	}
	// A giant embedded message is hard-capped.
	giant := errText(&op.Error{Code: op.CodeFailed, Msg: strings.Repeat("x", 5000)})
	if len(giant) > maxErrorText {
		t.Fatalf("giant error not capped: %d B", len(giant))
	}
}

// ---- progress ---------------------------------------------------------------

func TestEfficiencyProgressDedupe(t *testing.T) {
	dup := &op.Operation{
		Name: "test.dupprog", Summary: "Repeats one stage", Stream: true,
		Handler: func(_ context.Context, _ json.RawMessage, sink op.Sink) (any, error) {
			sink.Stage("build", op.StatusPass, 1, "")
			sink.Stage("build", op.StatusPass, 1, "") // identical — deduped
			sink.Stage("build", op.StatusPass, 1, "") // identical — deduped
			sink.Stage("test", op.StatusPass, 2, strings.Repeat("y", 900))
			return map[string]any{"ok": true}, nil
		},
	}
	ds, _ := stubDaemon(t, dup)
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
		Name: "test_dupprog", Arguments: map[string]any{"project_code": "TEST"}, Meta: mcp.Meta{"progressToken": "p"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal("op failed")
	}
	// Drain the client's async notification loop before counting.
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
	for i := 1; i < len(msgs); i++ {
		if msgs[i] == msgs[i-1] {
			t.Fatalf("consecutive duplicate progress notification: %q", msgs[i])
		}
	}
	for _, m := range msgs {
		if len(m) > maxProgressText {
			t.Fatalf("progress message %d B exceeds %d", len(m), maxProgressText)
		}
	}
	var dups int
	for i := 1; i < len(msgs); i++ {
		if msgs[i] == "build pass" && msgs[i-1] == "build pass" {
			dups++
		}
	}
	if dups != 0 {
		t.Fatalf("identical stage emitted %d extra times", dups)
	}
}

// ---- artifacts --------------------------------------------------------------

func TestEfficiencyArtifactNoInline(t *testing.T) {
	shot := &op.Operation{
		Name: "test.shot", Summary: "Artifact-by-reference", Stream: true,
		Handler: func(_ context.Context, _ json.RawMessage, sink op.Sink) (any, error) {
			sink.Artifact("image", "shot", "/tmp/shot-starting-tower-30.png")
			return map[string]any{
				"case": "starting-tower", "ticks": 30, "region": "full",
				"output": "/tmp/shot-starting-tower-30.png", "bytes": 1070828,
			}, nil
		},
	}
	ds, _ := stubDaemon(t, shot)
	registerProject(t, "TEST", "test-proj")
	cs := mcpClient(t, ds)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "test_shot", Arguments: map[string]any{"project_code": "TEST"}})
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(res)
	if len(wire) > 1024 {
		t.Fatalf("artifact result %d B — large data must stay by reference", len(wire))
	}
	s := string(wire)
	for _, bad := range []string{"data:image", "base64", "iVBOR", "/9j/"} {
		if strings.Contains(s, bad) {
			t.Fatalf("artifact result inlines binary data (%q)", bad)
		}
	}
}

// ---- representative call budgets --------------------------------------------

// repDaemon registers stub ops returning realistic Spin Tower-shaped results
// so serialized CallToolResult sizes are gated deterministically.
func repDaemon(t *testing.T) *daemon.Server {
	t.Helper()
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	reg := op.NewRegistry()
	obj := []byte(`{"type":"object","$id":"rep/x"}`)
	reps := map[string]any{
		"rep.project_info": map[string]any{
			"root": "/home/u/git/td-game", "id": "spin-tower", "type": "web",
			"contract":     "game-forge/v1",
			"capabilities": []string{"adapter", "browser", "gpu", "profile", "scenario", "server", "visual"},
			"adapters": []any{map[string]any{
				"name": "headless", "command": []string{"node", "scripts/gf-adapter.mjs"}, "protocol": "game-forge-adapter/v1"}},
		},
		"rep.scenario_list": map[string]any{"scenarios": []any{
			map[string]any{"id": "weapons", "summary": "Weapon firing arcs", "params": []any{
				map[string]any{"name": "arcs", "kind": "int", "default": "3", "min": 1, "max": 8, "help": "arc count"}}},
			map[string]any{"id": "floors", "summary": "Floor rotation", "params": []any{}},
			map[string]any{"id": "waves", "summary": "Wave pressure", "params": []any{}},
			map[string]any{"id": "progression", "summary": "Economy", "params": []any{}},
		}},
		"rep.scenario_run": map[string]any{
			"scenario": "weapons", "passed": true, "status": "PASS",
			"digest":  "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90",
			"simSeed": 42, "worldSeed": 7, "ticks": 120, "browser": false,
			"checks": []any{
				map[string]any{"name": "fires", "ok": true},
				map[string]any{"name": "hits", "ok": true, "detail": "12 hits"},
				map[string]any{"name": "arc", "ok": true, "detail": "3 arcs bounded"},
			},
		},
		"rep.scenario_compare": map[string]any{"compared": 4, "differing": 0, "results": []any{
			map[string]any{"id": "floors", "match": true, "digest": "aa55aa55aa55aa55aa55aa55aa55aa55"},
			map[string]any{"id": "progression", "match": true, "digest": "bb66bb66bb66bb66bb66bb66bb66bb66"},
			map[string]any{"id": "waves", "match": true, "digest": "cc77cc77cc77cc77cc77cc77cc77cc77"},
			map[string]any{"id": "weapons", "match": true, "digest": "dd88dd88dd88dd88dd88dd88dd88dd88"},
		}},
		"rep.visual_shot": map[string]any{
			"case": "starting-tower", "ticks": 30, "region": "full",
			"output": "/tmp/shot-starting-tower-30.png", "bytes": 1070828},
		"rep.browser_errors": map[string]any{"ok": true, "messages": []any{}},
		"rep.resource_list": map[string]any{"resources": []any{
			map[string]any{"id": "server-spin-tower-37de03-dev-3152182", "kind": "server",
				"provider": "local", "project": "spin-tower", "projectKey": "spin-tower-37de03",
				"pid": 3152182, "state": "active", "expires": "2026-09-20T12:00:00Z", "runId": "r_a1b2c3_4"},
			map[string]any{"id": "browser-game-forge-spin-tower-37de03", "kind": "browser",
				"provider": "agent-browser", "project": "spin-tower", "projectKey": "spin-tower-37de03",
				"namespace": "game-forge-spin-tower-37de03", "host": "wsl", "state": "active",
				"expires": "2026-09-20T12:00:00Z", "runId": "r_a1b2c3_4"},
		}},
		"rep.gpu_run": map[string]any{
			"ok": true, "renderer": "ANGLE (NVIDIA, NVIDIA GeForce RTX 4070)", "vendor": "NVIDIA",
			"software": false, "benchmark": map[string]any{
				"scenario": "high-shot-clearance", "frames": 120, "meanFrameMs": 2.1, "draws": 200, "tris": 8568}},
		"rep.profile_run": map[string]any{"profile": "verify_full", "ok": true, "ms": 61234, "stages": []any{
			map[string]any{"name": "verify/flow", "ok": true, "ms": 8200},
			map[string]any{"name": "verify/sweep", "ok": true, "ms": 31400, "detail": "27 cases"},
			map[string]any{"name": "verify/errors", "ok": true, "ms": 3100},
			map[string]any{"name": "selfcheck", "ok": true, "ms": 900},
			map[string]any{"name": "present-check", "ok": true, "ms": 2400},
			map[string]any{"name": "lifecycle", "ok": true, "ms": 5100},
			map[string]any{"name": "sound", "ok": true, "ms": 4300},
			map[string]any{"name": "prod", "ok": true, "ms": 5800},
		}},
	}
	names := []string{}
	for n := range reps {
		names = append(names, n)
	}
	for _, name := range names {
		payload := reps[name]
		o := &op.Operation{
			Name: name, Summary: "representative " + name, Stream: true,
			Handler: func(context.Context, json.RawMessage, op.Sink) (any, error) { return payload, nil },
		}
		in := []byte(strings.ReplaceAll(string(obj), "rep/x", name+"/in"))
		out := []byte(strings.ReplaceAll(string(obj), "rep/x", name+"/out"))
		if err := reg.AddRaw(o, in, out); err != nil {
			t.Fatal(err)
		}
	}
	return daemon.NewServer(core.NewRuntime(false), reg, nil)
}

func TestEfficiencyRepresentativeResults(t *testing.T) {
	ds := repDaemon(t)
	registerProject(t, "REP", "rep-proj")
	cs := mcpClient(t, ds)
	b, err := LoadBudget()
	if err != nil {
		t.Fatal(err)
	}
	// rep_* stub tools stand in for the canonical ops of the same shape.
	repToBudget := map[string]string{
		"rep_project_info":     "project_info",
		"rep_scenario_list":    "scenario_list",
		"rep_scenario_run":     "scenario_run",
		"rep_scenario_compare": "scenario_compare",
		"rep_visual_shot":      "visual_shot",
		"rep_browser_errors":   "browser_errors",
		"rep_resource_list":    "resource_list",
		"rep_gpu_run":          "gpu_run",
		"rep_profile_run":      "profile_run",
	}
	for stub, budgetKey := range repToBudget {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: stub, Arguments: map[string]any{"project_code": "REP"}})
		if err != nil {
			t.Fatalf("%s: %v", stub, err)
		}
		if res.IsError {
			t.Fatalf("%s failed: %+v", stub, res.Content)
		}
		got := serialized(t, res)
		limit := b.RepresentativeResultBytes[budgetKey]
		if limit == 0 {
			t.Errorf("no representative budget for %s", budgetKey)
			continue
		}
		if got > limit {
			t.Errorf("%s result %d B exceeds budget %d", budgetKey, got, limit)
		}
		t.Logf("%-22s %5d B (budget %d)", budgetKey, got, limit)
	}
}

// ---- schema projection ------------------------------------------------------

func TestCompactSchema(t *testing.T) {
	in := json.RawMessage(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$id": "game-forge://ops/x/input",
		"type": "object",
		"required": ["id"],
		"properties": {"id": {"type": "string", "minLength": 1}, "ticks": {"type": "integer", "minimum": 0}},
		"additionalProperties": false
	}`)
	out := compactSchema(in)
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["$schema"] != nil || doc["$id"] != nil {
		t.Fatalf("wire metadata survived projection: %v", doc)
	}
	// Validation-relevant semantics are preserved verbatim.
	if doc["type"] != "object" || doc["additionalProperties"] != false {
		t.Fatalf("validation semantics lost: %v", doc)
	}
	req, _ := doc["required"].([]any)
	if len(req) != 1 || req[0] != "id" {
		t.Fatalf("required lost: %v", doc)
	}
	props, _ := doc["properties"].(map[string]any)
	if len(props) != 2 {
		t.Fatalf("properties lost: %v", doc)
	}
	// A document containing $ref is left untouched: $id participates in
	// resolution there.
	withRef := json.RawMessage(`{"$id":"x","$ref":"#/$defs/a","$defs":{"a":{"$id":"b","type":"string"}}}`)
	if string(compactSchema(withRef)) != string(withRef) {
		t.Fatal("$ref document was rewritten")
	}
	// Canonical schemas carry no $ref — the projection applies to all of them.
	for _, name := range []string{"scenario.run", "visual.shot", "gpu.run"} {
		sin, sout, err := schemasForTest(name)
		if err != nil {
			t.Fatal(err)
		}
		if containsRefJSON(sin) || containsRefJSON(sout) {
			t.Fatalf("canonical schema %s gained $ref — projection must be reviewed", name)
		}
	}
}

// TestInjectProjectCode pins the transport-property injection: required,
// compact, and never mutating the canonical document.
func TestInjectProjectCode(t *testing.T) {
	in := json.RawMessage(`{"$id":"x","type":"object","required":["id"],"properties":{"id":{"type":"string"}}}`)
	out := injectProjectCode(compactSchema(in))
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	props, _ := doc["properties"].(map[string]any)
	pc, _ := props["project_code"].(map[string]any)
	if pc["type"] != "string" {
		t.Fatalf("project_code projection wrong: %v", pc)
	}
	// Compact: no long repeated description.
	if len(pc) > 1 {
		t.Fatalf("project_code carries more than its type: %v", pc)
	}
	req, _ := doc["required"].([]any)
	var found bool
	for _, r := range req {
		if r == "project_code" {
			found = true
		}
	}
	if !found {
		t.Fatal("project_code not required")
	}
	// Canonical document untouched.
	var canon map[string]any
	json.Unmarshal(in, &canon)
	if canon["$id"] == nil {
		t.Fatal("canonical schema was mutated")
	}
	// Schemas without properties/required still gain both.
	out2 := injectProjectCode(compactSchema(json.RawMessage(`{"type":"object","$id":"y"}`)))
	var doc2 map[string]any
	json.Unmarshal(out2, &doc2)
	if doc2["properties"] == nil || doc2["required"] == nil {
		t.Fatalf("injection failed on sparse schema: %v", doc2)
	}
}

func containsRefJSON(raw json.RawMessage) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	return hasRef(v)
}

func schemasForTest(name string) (in, out json.RawMessage, err error) {
	ib, ob, err := schemas.Operation(name)
	return json.RawMessage(ib), json.RawMessage(ob), err
}

// ---- audit ------------------------------------------------------------------

func TestAuditReportPasses(t *testing.T) {
	_, disp := realDaemon(t)
	registerProject(t, "TEST", "test-proj")
	rep, err := Audit(context.Background(), disp, "TEST")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "PASS" {
		for _, g := range rep.Gates {
			if !g.Pass {
				t.Errorf("gate %s: %d > %d %s", g.Name, g.Actual, g.Limit, g.Unit)
			}
		}
		t.Fatal("audit failed")
	}
	if rep.Tools == 0 || rep.CatalogBytes == 0 || rep.ModelSurfaceBytes == 0 {
		t.Fatalf("audit missing measurements: %+v", rep)
	}
	if rep.ProjectCodeBytes == 0 {
		t.Fatal("audit did not measure project_code overhead")
	}
	if rep.InitHTTPRequests > 2 {
		t.Fatalf("audit init made %d requests", rep.InitHTTPRequests)
	}
	if !rep.ProbeOK {
		t.Fatal("probe call did not succeed through the registered project")
	}
	if len(rep.Largest) == 0 {
		t.Fatal("audit did not report largest tools")
	}
	// JSON form is stable enough for CI: required keys exist.
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"status", "tools", "catalogBytes", "modelSurfaceBytes",
		"projectCodeBytes", "initHTTPRequests", "warmInitMs", "gates", "largestTools"} {
		if doc[k] == nil {
			t.Errorf("audit JSON missing key %q", k)
		}
	}
}

// TestEfficiencyWarmLatency measures warm init and a trivial call through the
// real daemon dispatcher. Hard ceilings gate; soft targets are logged.
func TestEfficiencyWarmLatency(t *testing.T) {
	ds, _ := realDaemon(t)
	registerProject(t, "TEST", "test-proj")
	b, _ := LoadBudget()
	t0 := time.Now()
	fe, err := New(context.Background(), ds, Options{})
	if err != nil {
		t.Fatal(err)
	}
	initMs := time.Since(t0).Milliseconds()
	if initMs > int64(b.MaxWarmInitMs) {
		t.Fatalf("warm init %d ms exceeds hard ceiling %d", initMs, b.MaxWarmInitMs)
	}
	if initMs > int64(b.TargetWarmInitMs) {
		t.Logf("warm init %d ms above soft target %d (under ceiling)", initMs, b.TargetWarmInitMs)
	}
	st, ct := mcp.NewInMemoryTransports()
	go fe.Run(context.Background(), st)
	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	cs, err := c.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	var lat []int64
	for i := 0; i < 9; i++ {
		s := time.Now()
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "resource_list", Arguments: map[string]any{"project_code": "TEST"}})
		lat = append(lat, time.Since(s).Milliseconds())
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("trivial call failed: %+v", res.Content)
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p50 := lat[len(lat)/2]
	p95 := lat[len(lat)-1]
	t.Logf("init=%dms call p50=%dms p95=%dms", initMs, p50, p95)
	if p95 > int64(b.MaxTrivialCallMs) {
		t.Fatalf("trivial call p95 %d ms exceeds hard ceiling %d", p95, b.MaxTrivialCallMs)
	}
	if p50 > int64(b.TargetTrivialCallMs) {
		t.Logf("call p50 %d ms above soft target %d (under ceiling)", p50, b.TargetTrivialCallMs)
	}
}
