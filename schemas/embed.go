// Package schemas holds the canonical JSON Schema contracts for the Game Forge
// daemon control protocol and for every operation the Core exposes.
//
// The schemas are the source of truth for the wire contract. They are checked
// in, embedded into the binary, and resolved entirely from memory: nothing here
// fetches a remote $ref at runtime.
//
// Layout:
//
//	schemas/protocol/request.schema.json   the v1 request envelope
//	schemas/protocol/response.schema.json  the non-streaming reply envelope
//	schemas/protocol/event.schema.json     one NDJSON streamed event
//	schemas/protocol/error.schema.json     the structured error shape
//	schemas/ops/<op>.input.schema.json     one operation's arguments
//	schemas/ops/<op>.output.schema.json    one operation's result
//
// The same operation schemas are what a future MCP frontend should advertise as
// tool input/output schemas, so no second schema copy should ever be written.
package schemas

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed protocol ops
var files embed.FS

// Protocol constants describing the control protocol this package defines.
const (
	ProtocolDir = "protocol"
	OpsDir      = "ops"
	// Protocol is the daemon protocol identifier.
	Protocol = "game-forge-daemon/v1"
	// Version is the wire version carried in every request envelope.
	Version = 1
)

// Read returns one embedded schema file by its path relative to schemas/.
func Read(path string) ([]byte, error) {
	b, err := files.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read schema %s: %w", path, err)
	}
	return b, nil
}

// ProtocolSchema returns one protocol schema, e.g. "request".
func ProtocolSchema(name string) ([]byte, error) {
	return Read(ProtocolDir + "/" + name + ".schema.json")
}

// Operation names every operation that has a checked-in contract.
func OperationNames() []string {
	entries, err := fs.ReadDir(files, OpsDir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".input.schema.json")
		if name == e.Name() {
			name = strings.TrimSuffix(e.Name(), ".output.schema.json")
		}
		if name != e.Name() {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Operation returns an operation's input and output schemas.
func Operation(name string) (input, output []byte, err error) {
	input, err = Read(OpsDir + "/" + name + ".input.schema.json")
	if err != nil {
		return nil, nil, err
	}
	output, err = Read(OpsDir + "/" + name + ".output.schema.json")
	if err != nil {
		return nil, nil, err
	}
	return input, output, nil
}
