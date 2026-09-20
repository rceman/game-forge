package mcpfrontend

// MCP efficiency audit.
//
// A coding agent pays for the MCP surface in context and latency, so the
// frontend's wire efficiency is part of correctness. This file measures the
// REAL serialized MCP representation — a live tools/list through the official
// SDK client and a real tool call through the daemon — and compares it
// against the checked-in budget. The byte metric is UTF-8 serialized bytes:
// deterministic and model-independent. estimatedTokens = ceil(bytes/4) is a
// human-friendly approximation only, never an exact token count.

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed efficiency-budget.json
var budgetJSON []byte

// Budget is the checked-in MCP efficiency contract. Every limit is named so a
// regression report explains itself. The file is never rewritten by tests or
// the audit: raising a limit is a deliberate source change that must say why
// in the commit.
type Budget struct {
	// Catalog/model surface.
	MaxCatalogBytes            int `json:"maxCatalogBytes"`
	MaxModelSurfaceBytes       int `json:"maxModelSurfaceBytes"`
	MaxDescriptionBytesPerTool int `json:"maxDescriptionBytesPerTool"`
	MaxToolBytes               int `json:"maxToolBytes"`
	MaxAvgToolBytes            int `json:"maxAvgToolBytes"`
	// Initialization.
	MaxInitHTTPRequests int `json:"maxInitHTTPRequests"`
	MaxWarmInitMs       int `json:"maxWarmInitMs"`
	MaxWarmReadyMs      int `json:"maxWarmReadyMs"`
	// Call results.
	MaxStructuredTextDuplicateBytes int `json:"maxStructuredTextDuplicateBytes"`
	MaxSuccessTextBytes             int `json:"maxSuccessTextBytes"`
	MaxErrorTextBytes               int `json:"maxErrorTextBytes"`
	MaxDuplicateProgressEvents      int `json:"maxDuplicateProgressEvents"`
	MaxResultBytes                  int `json:"maxResultBytes"`
	// Latency: hard ceilings gate; soft targets are reported, never failed
	// on alone.
	MaxTrivialCallMs    int `json:"maxTrivialCallMs"`
	TargetWarmInitMs    int `json:"targetWarmInitMs"`
	TargetTrivialCallMs int `json:"targetTrivialCallMs"`
	// RepresentativeResultBytes bounds the serialized CallToolResult for
	// deterministic representative calls (stub ops in tests, real ops in the
	// integration audit). Keys are MCP tool names. Headroom is ~15-25% over
	// measured baselines — enough for real ids/paths, tight enough that raw
	// logs, duplicated JSON or giant diagnostics fail.
	RepresentativeResultBytes map[string]int `json:"representativeResultBytes"`
}

// LoadBudget parses the checked-in efficiency budget.
func LoadBudget() (*Budget, error) {
	var b Budget
	if err := json.Unmarshal(budgetJSON, &b); err != nil {
		return nil, fmt.Errorf("efficiency budget: %w", err)
	}
	return &b, nil
}

// ToolSize is one tool's serialized definition cost.
type ToolSize struct {
	Name string `json:"name"`
	Size int    `json:"bytes"`
}

// Gate is one named budget check.
type Gate struct {
	Name   string `json:"name"`
	Limit  int    `json:"limit"`
	Actual int    `json:"actual"`
	Unit   string `json:"unit"`
	Pass   bool   `json:"pass"`
}

// Report is the measured MCP efficiency surface plus gate results.
type Report struct {
	Status string `json:"status"`

	Tools             int `json:"tools"`
	CatalogBytes      int `json:"catalogBytes"`
	ModelSurfaceBytes int `json:"modelSurfaceBytes"`
	NamesBytes        int `json:"namesBytes"`
	DescriptionsBytes int `json:"descriptionsBytes"`
	InputSchemaBytes  int `json:"inputSchemaBytes"`
	OutputSchemaBytes int `json:"outputSchemaBytes"`
	// ProjectCodeBytes is the serialized cost of the injected project_code
	// transport property across all tools.
	ProjectCodeBytes       int `json:"projectCodeBytes"`
	MaxDescriptionPerTool  int `json:"maxDescriptionPerTool"`
	AvgToolBytes           int `json:"avgToolBytes"`
	EstimatedSurfaceTokens int `json:"estimatedSurfaceTokens"`

	// InitHTTPRequests counts daemon HTTP round trips during frontend
	// construction. The in-process dispatcher (canonical /mcp) uses 0; the
	// HTTP adapter uses 1 catalog request.
	InitHTTPRequests int   `json:"initHTTPRequests"`
	WarmInitMs       int64 `json:"warmInitMs"`
	WarmReadyMs      int64 `json:"warmReadyMs"`

	// Probe is one trivial real call (resource_list: side-effect-free, no
	// browser or server needed) measuring the result envelope. When no
	// project is registered the probe exercises the error path instead —
	// still a real serialized CallToolResult.
	ProbeTool           string `json:"probeTool"`
	ProbeProject        string `json:"probeProject"`
	ProbeOK             bool   `json:"probeOK"`
	ProbeResultBytes    int    `json:"probeResultBytes"`
	ProbeStructuredByte int    `json:"probeStructuredBytes"`
	ProbeTextBytes      int    `json:"probeTextBytes"`
	ProbeDuplicateBytes int    `json:"probeDuplicateBytes"`
	ProbeProgressEvents int    `json:"probeProgressEvents"`
	ProbeProgressBytes  int    `json:"probeProgressBytes"`
	ProbeP50Ms          int64  `json:"probeP50Ms"`
	ProbeP95Ms          int64  `json:"probeP95Ms"`

	Largest []ToolSize `json:"largestTools"`
	Gates   []Gate     `json:"gates"`
}

// Audit measures the MCP efficiency surface against the checked-in budget.
// Discovery is metadata-only: no tool that starts a browser, dev server or
// GPU probe is invoked. The call probe is resource_list — read-only — routed
// to probeCode; with no registered project the probe measures the
// unknown_project error path, which is still a bounded result.
func Audit(ctx context.Context, disp Dispatcher, probeCode string) (*Report, error) {
	b, err := LoadBudget()
	if err != nil {
		return nil, err
	}
	rep := &Report{Status: "PASS"}

	reqs0 := disp.Requests()
	t0 := time.Now()
	fe, err := New(ctx, disp, Options{})
	if err != nil {
		return nil, err
	}
	rep.WarmInitMs = time.Since(t0).Milliseconds()
	rep.InitHTTPRequests = int(disp.Requests() - reqs0)

	st, ct := mcp.NewInMemoryTransports()
	go fe.Run(ctx, st)
	// Count real progress notifications so the probe can report their volume.
	var progMu struct {
		n     int
		bytes int
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "game-forge-audit", Version: Version}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			progMu.n++
			progMu.bytes += len(r.Params.Message)
		},
	})
	ready0 := time.Now()
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		return nil, err
	}
	defer cs.Close()
	tl, err := cs.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	rep.WarmReadyMs = time.Since(ready0).Milliseconds()

	wire, _ := json.Marshal(tl)
	rep.Tools = len(tl.Tools)
	rep.CatalogBytes = len(wire)
	var maxDesc, totalTool int
	for _, t := range tl.Tools {
		rep.NamesBytes += len(t.Name)
		rep.DescriptionsBytes += len(t.Description)
		if len(t.Description) > maxDesc {
			maxDesc = len(t.Description)
		}
		ib, _ := json.Marshal(t.InputSchema)
		rep.InputSchemaBytes += len(ib)
		ob, _ := json.Marshal(t.OutputSchema)
		rep.OutputSchemaBytes += len(ob)
		tb, _ := json.Marshal(t)
		totalTool += len(tb)
		rep.Largest = append(rep.Largest, ToolSize{Name: t.Name, Size: len(tb)})
	}
	rep.MaxDescriptionPerTool = maxDesc
	if rep.Tools > 0 {
		rep.AvgToolBytes = totalTool / rep.Tools
	}
	// project_code overhead = the injected transport property's serialized
	// cost across the whole catalog (input schema delta per tool).
	for _, meta := range fe.metas {
		rep.ProjectCodeBytes += len(injectProjectCode(compactSchema(meta.Input))) - len(compactSchema(meta.Input))
	}
	// Model surface = what an LLM needs to choose and call a tool:
	// name + description + input schema (output schemas are daemon-side
	// validation metadata in compact mode and excluded).
	rep.ModelSurfaceBytes = rep.NamesBytes + rep.DescriptionsBytes + rep.InputSchemaBytes
	rep.EstimatedSurfaceTokens = (rep.ModelSurfaceBytes + 3) / 4
	sort.Slice(rep.Largest, func(i, j int) bool { return rep.Largest[i].Size > rep.Largest[j].Size })
	if len(rep.Largest) > 5 {
		rep.Largest = rep.Largest[:5]
	}

	rep.probe(ctx, cs, probeCode, &progMu)

	rep.Gates = []Gate{
		gate("catalogBytes", b.MaxCatalogBytes, rep.CatalogBytes),
		gate("modelSurfaceBytes", b.MaxModelSurfaceBytes, rep.ModelSurfaceBytes),
		gate("descriptionBytesPerTool", b.MaxDescriptionBytesPerTool, rep.MaxDescriptionPerTool),
		gate("toolBytes", b.MaxToolBytes, maxTool(rep.Largest)),
		gate("avgToolBytes", b.MaxAvgToolBytes, rep.AvgToolBytes),
		gate("initHTTPRequests", b.MaxInitHTTPRequests, rep.InitHTTPRequests),
		gate("warmInitMs", b.MaxWarmInitMs, int(rep.WarmInitMs)),
		gate("warmReadyMs", b.MaxWarmReadyMs, int(rep.WarmReadyMs)),
		gate("structuredTextDuplicateBytes", b.MaxStructuredTextDuplicateBytes, rep.ProbeDuplicateBytes),
		gate("successTextBytes", b.MaxSuccessTextBytes, rep.ProbeTextBytes),
		gate("resultBytes", b.MaxResultBytes, rep.ProbeResultBytes),
		gate("trivialCallP95Ms", b.MaxTrivialCallMs, int(rep.ProbeP95Ms)),
	}
	for _, g := range rep.Gates {
		if !g.Pass {
			rep.Status = "FAIL"
		}
	}
	return rep, nil
}

// probe times trivial resource_list calls and inspects the real serialized
// CallToolResult: duplication, text fallback and progress volume. prog carries
// the client's progress-notification counters.
func (rep *Report) probe(ctx context.Context, cs *mcp.ClientSession, code string, prog *struct {
	n     int
	bytes int
}) {
	rep.ProbeTool = "resource_list"
	rep.ProbeProject = code
	var lat []int64
	const n = 7
	for i := 0; i < n; i++ {
		t0 := time.Now()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "resource_list",
			Arguments: map[string]any{"project_code": code},
			Meta:      mcp.Meta{"progressToken": "audit-probe"},
		})
		lat = append(lat, time.Since(t0).Milliseconds())
		if err != nil || res == nil {
			continue
		}
		if i == 0 {
			rep.ProbeOK = !res.IsError
			wire, _ := json.Marshal(res)
			rep.ProbeResultBytes = len(wire)
			sb, _ := json.Marshal(res.StructuredContent)
			if res.StructuredContent == nil {
				sb = nil
			}
			rep.ProbeStructuredByte = len(sb)
			for _, c := range res.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					rep.ProbeTextBytes += len(tc.Text)
				}
			}
			// Duplication = bytes of the structured payload repeated in text.
			if rep.ProbeTextBytes > 0 && sb != nil {
				if tc, ok := res.Content[0].(*mcp.TextContent); ok && tc.Text == string(sb) {
					rep.ProbeDuplicateBytes = len(sb)
				}
			}
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	if len(lat) > 0 {
		rep.ProbeP50Ms = lat[len(lat)/2]
		rep.ProbeP95Ms = lat[len(lat)-1]
	}
	rep.ProbeProgressEvents = prog.n
	rep.ProbeProgressBytes = prog.bytes
}

func gate(name string, limit, actual int) Gate {
	return Gate{Name: name, Limit: limit, Actual: actual, Unit: unitOf(name), Pass: actual <= limit}
}

func unitOf(name string) string {
	switch name {
	case "warmInitMs", "warmReadyMs", "trivialCallP95Ms":
		return "ms"
	case "initHTTPRequests":
		return "requests"
	default:
		return "bytes"
	}
}

func maxTool(l []ToolSize) int {
	if len(l) == 0 {
		return 0
	}
	return l[0].Size
}

// Render prints the compact human audit. It never dumps schemas.
func (r *Report) Render(w io.Writer) int {
	fmt.Fprintf(w, "MCP efficiency\n")
	fmt.Fprintf(w, "  tools:             %d\n", r.Tools)
	fmt.Fprintf(w, "  model surface:     %s (~%d tok)\n", size(r.ModelSurfaceBytes), r.EstimatedSurfaceTokens)
	fmt.Fprintf(w, "  full catalog:      %s\n", size(r.CatalogBytes))
	fmt.Fprintf(w, "  project_code cost: %s\n", size(r.ProjectCodeBytes))
	fmt.Fprintf(w, "  descriptions:      %s\n", size(r.DescriptionsBytes))
	fmt.Fprintf(w, "  init HTTP calls:   %d\n", r.InitHTTPRequests)
	fmt.Fprintf(w, "  warm init/ready:   %d ms / %d ms\n", r.WarmInitMs, r.WarmReadyMs)
	fmt.Fprintf(w, "  duplicate output:  %s\n", size(r.ProbeDuplicateBytes))
	fmt.Fprintf(w, "  probe %s[%s]: %s, p50 %d ms, p95 %d ms\n",
		r.ProbeTool, r.ProbeProject, size(r.ProbeResultBytes), r.ProbeP50Ms, r.ProbeP95Ms)
	for i, ts := range r.Largest {
		if i >= 3 {
			break
		}
		fmt.Fprintf(w, "  largest #%d:        %-18s %s\n", i+1, ts.Name, size(ts.Size))
	}
	for _, g := range r.Gates {
		mark := "ok"
		if !g.Pass {
			mark = "FAIL"
		}
		fmt.Fprintf(w, "  gate %-32s %4d / %-4d %s %s\n", g.Name, g.Actual, g.Limit, g.Unit, mark)
	}
	fmt.Fprintf(w, "  status:            %s\n", r.Status)
	if r.Status == "PASS" {
		return 0
	}
	return 1
}

// size renders a byte count compactly.
func size(b int) string {
	if b >= 1024 {
		return fmt.Sprintf("%.1f KiB", float64(b)/1024)
	}
	return fmt.Sprintf("%d B", b)
}
