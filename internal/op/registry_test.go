package op_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rceman/game-forge/internal/core"
	"github.com/rceman/game-forge/internal/op"
	"github.com/rceman/game-forge/schemas"
)

// buildRegistry wires the real operations exactly as the daemon does.
func buildRegistry(t *testing.T) *op.Registry {
	t.Helper()
	rt := core.NewRuntime(false)
	reg := op.NewRegistry()
	if err := core.Register(reg, rt); err != nil {
		t.Fatalf("register: %v", err)
	}
	return reg
}

// TestRegistrySchemaCoverage proves every public operation has both an input
// and an output schema, and that the checked-in schema set matches the
// registered operation set exactly.
func TestRegistrySchemaCoverage(t *testing.T) {
	reg := buildRegistry(t)
	if reg.Len() == 0 {
		t.Fatal("no operations registered")
	}
	for _, o := range reg.All() {
		if len(o.InputSchema()) == 0 {
			t.Errorf("operation %q has no input schema", o.Name)
		}
		if len(o.OutputSchema()) == 0 {
			t.Errorf("operation %q has no output schema", o.Name)
		}
	}
	// The schema directory and the registry must agree: no orphan schema, no
	// unregistered operation.
	schemaSet := map[string]bool{}
	for _, n := range schemas.OperationNames() {
		schemaSet[n] = true
	}
	for _, o := range reg.All() {
		if !schemaSet[o.Name] {
			t.Errorf("operation %q registered without a checked-in schema", o.Name)
		}
		delete(schemaSet, o.Name)
	}
	for orphan := range schemaSet {
		t.Errorf("schema for %q exists but no operation is registered", orphan)
	}
}

// TestRegistryNoDuplicateNames proves names are unique.
func TestRegistryNoDuplicateNames(t *testing.T) {
	reg := buildRegistry(t)
	seen := map[string]bool{}
	for _, n := range reg.Names() {
		if seen[n] {
			t.Errorf("duplicate operation name %q", n)
		}
		seen[n] = true
	}
	// Adding the same operation twice must fail.
	o, _ := reg.Lookup(reg.Names()[0])
	dup := &op.Operation{Name: o.Name, Handler: func(context.Context, json.RawMessage, op.Sink) (any, error) { return nil, nil }}
	if err := reg.AddRaw(dup, []byte(`{"type":"object","$id":"x/in"}`), []byte(`{"type":"object","$id":"x/out"}`)); err == nil {
		t.Error("expected duplicate registration to fail")
	}
}

// TestRegistryMetadataForMCP proves a future MCP frontend can enumerate every
// operation's name, summary and input schema without touching the CLI.
func TestRegistryMetadataForMCP(t *testing.T) {
	reg := buildRegistry(t)
	for _, o := range reg.All() {
		if o.Name == "" || o.Summary == "" {
			t.Errorf("operation %q lacks a name or summary", o.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(o.InputSchema(), &schema); err != nil {
			t.Errorf("operation %q input schema is not valid JSON: %v", o.Name, err)
		}
		// An MCP inputSchema must be a JSON Schema object.
		if schema["type"] != "object" {
			t.Errorf("operation %q input schema is not an object schema", o.Name)
		}
	}
}
