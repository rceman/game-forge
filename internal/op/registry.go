package op

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Operation is one externally callable Core capability: one canonical name, one
// input schema, one output schema, one handler.
type Operation struct {
	// Name is the canonical operation name, e.g. "scenario.run".
	Name string
	// Summary is a one-line description, suitable for CLI help or an MCP tool
	// description.
	Summary string
	// Stream reports whether the operation is long enough to be worth
	// streaming progress for. It never changes the result, only the transport.
	Stream bool
	// Handler is the operation's single Core implementation.
	Handler Handler

	inSchema  *jsonschema.Schema
	inRaw     json.RawMessage
	outRaw    json.RawMessage
	outSchema *jsonschema.Schema
}

// Registry is the set of operations a build exposes.
type Registry struct {
	order  []*Operation
	byName map[string]*Operation
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: map[string]*Operation{}}
}

// Add registers an operation, loading and compiling its schemas.
//
// It fails when the name is empty or duplicated, when either schema is missing,
// or when a schema does not compile. A missing contract is a build error, not a
// runtime surprise.
func (r *Registry) Add(o *Operation) error {
	in, out, err := loadSchemas(o.Name)
	if err != nil {
		return err
	}
	return r.AddRaw(o, in, out)
}

// AddRaw registers an operation with explicit input/output schema documents.
// The normal path resolves them from the checked-in schema set via Add; this
// exists so tests and embedders can register operations whose schemas are not
// checked in under schemas/ops.
func (r *Registry) AddRaw(o *Operation, in, out json.RawMessage) error {
	if o.Name == "" {
		return fmt.Errorf("operation has no name")
	}
	if o.Handler == nil {
		return fmt.Errorf("operation %q has no handler", o.Name)
	}
	if _, dup := r.byName[o.Name]; dup {
		return fmt.Errorf("operation %q is registered twice", o.Name)
	}
	o.inRaw, o.outRaw = in, out
	inSch, err := compile(in)
	if err != nil {
		return fmt.Errorf("operation %q input: %w", o.Name, err)
	}
	outSch, err := compile(out)
	if err != nil {
		return fmt.Errorf("operation %q output: %w", o.Name, err)
	}
	o.inSchema, o.outSchema = inSch, outSch
	r.byName[o.Name] = o
	r.order = append(r.order, o)
	return nil
}

// Lookup returns an operation by canonical name.
func (r *Registry) Lookup(name string) (*Operation, bool) {
	o, ok := r.byName[name]
	return o, ok
}

// Names returns every registered operation name, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.order))
	for _, o := range r.order {
		out = append(out, o.Name)
	}
	sort.Strings(out)
	return out
}

// All returns every registered operation in registration order.
func (r *Registry) All() []*Operation {
	out := make([]*Operation, len(r.order))
	copy(out, r.order)
	return out
}

// Len returns the number of registered operations.
func (r *Registry) Len() int { return len(r.order) }
