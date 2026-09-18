package op

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/rceman/game-forge/schemas"
)

var printer = message.NewPrinter(language.English)

// compile builds a validator from embedded schema bytes. Schemas are resolved
// from memory only: no remote $ref is ever fetched.
func compile(raw []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema is not an object")
	}
	id, _ := obj["$id"].(string)
	if id == "" {
		return nil, fmt.Errorf("schema has no $id")
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(id, doc); err != nil {
		return nil, fmt.Errorf("add schema %s: %w", id, err)
	}
	sch, err := c.Compile(id)
	if err != nil {
		return nil, fmt.Errorf("compile schema %s: %w", id, err)
	}
	return sch, nil
}

// Protocol holds the compiled protocol-level schemas.
type Protocol struct {
	Request  *jsonschema.Schema
	Response *jsonschema.Schema
	Event    *jsonschema.Schema
	Error    *jsonschema.Schema
}

var (
	protocolOnce sync.Once
	protocolSch  *Protocol
	protocolErr  error
)

// ProtocolSchemas compiles and caches the protocol schemas.
func ProtocolSchemas() (*Protocol, error) {
	protocolOnce.Do(func() {
		p := &Protocol{}
		for name, dst := range map[string]**jsonschema.Schema{
			"request":  &p.Request,
			"response": &p.Response,
			"event":    &p.Event,
			"error":    &p.Error,
		} {
			raw, err := schemas.ProtocolSchema(name)
			if err != nil {
				protocolErr = err
				return
			}
			sch, err := compile(raw)
			if err != nil {
				protocolErr = err
				return
			}
			*dst = sch
		}
		protocolSch = p
	})
	return protocolSch, protocolErr
}

// validate runs one schema and converts any failure into the compact wire
// error. Validator internals are never dumped: one path and one message.
func validate(sch *jsonschema.Schema, v any, code string) *Error {
	if sch == nil {
		return &Error{Code: CodeInternal, Msg: "schema is not compiled"}
	}
	err := sch.Validate(v)
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		leaf := ve
		for len(leaf.Causes) > 0 {
			leaf = leaf.Causes[0]
		}
		path := ""
		if len(leaf.InstanceLocation) > 0 {
			path = "/" + strings.Join(leaf.InstanceLocation, "/")
		}
		msg := leaf.ErrorKind.LocalizedString(printer)
		return &Error{Code: code, Path: path, Msg: msg}
	}
	return &Error{Code: code, Msg: err.Error()}
}

// decode converts arbitrary JSON bytes into the value shape the validator
// expects.
func decode(raw []byte) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(raw))
}

// toJSONValue round-trips a Go value through JSON so validation sees exactly
// what a client would.
func toJSONValue(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return decode(raw)
}

// ValidateRequest checks a request envelope.
func ValidateRequest(v any) *Error {
	p, err := ProtocolSchemas()
	if err != nil {
		return &Error{Code: CodeInternal, Msg: err.Error()}
	}
	return validate(p.Request, v, CodeInvalidRequest)
}

// ValidateResponse checks a non-streaming reply envelope.
func ValidateResponse(v any) *Error {
	p, err := ProtocolSchemas()
	if err != nil {
		return &Error{Code: CodeInternal, Msg: err.Error()}
	}
	val, err := toJSONValue(v)
	if err != nil {
		return &Error{Code: CodeInternal, Msg: "encode: " + err.Error()}
	}
	return validate(p.Response, val, CodeInternal)
}

// ValidateEvent checks one streamed event.
func ValidateEvent(v any) *Error {
	p, err := ProtocolSchemas()
	if err != nil {
		return &Error{Code: CodeInternal, Msg: err.Error()}
	}
	val, err := toJSONValue(v)
	if err != nil {
		return &Error{Code: CodeInternal, Msg: "encode: " + err.Error()}
	}
	return validate(p.Event, val, CodeInternal)
}

// ValidateError checks a structured error.
func ValidateError(v any) *Error {
	p, err := ProtocolSchemas()
	if err != nil {
		return &Error{Code: CodeInternal, Msg: err.Error()}
	}
	val, err := toJSONValue(v)
	if err != nil {
		return &Error{Code: CodeInternal, Msg: "encode: " + err.Error()}
	}
	return validate(p.Error, val, CodeInternal)
}

// ValidateArgs checks an operation's arguments.
func (o *Operation) ValidateArgs(raw json.RawMessage) *Error {
	v, err := decode(raw)
	if err != nil {
		return &Error{Code: CodeInvalidArgs, Msg: "arguments are not valid JSON"}
	}
	return validate(o.inSchema, v, CodeInvalidArgs)
}

// ValidateOutput checks an operation's result before it is sent to a client.
// A schema-invalid Core result is a contract bug, not a client error.
func (o *Operation) ValidateOutput(v any) *Error {
	val, err := toJSONValue(v)
	if err != nil {
		return &Error{Code: CodeInternal, Msg: "encode output: " + err.Error()}
	}
	return validate(o.outSchema, val, CodeInvalidOutput)
}

// InputSchema returns the operation's canonical input schema bytes.
func (o *Operation) InputSchema() json.RawMessage { return o.inRaw }

// OutputSchema returns the operation's canonical output schema bytes.
func (o *Operation) OutputSchema() json.RawMessage { return o.outRaw }

// ValidateValue checks an arbitrary value against an operation schema kind.
// It exists so tests can prove validation rejects bad data.
func ValidateValue(schemaKind string, raw []byte, v any) *Error {
	sch, err := compile(raw)
	if err != nil {
		return &Error{Code: CodeInternal, Msg: err.Error()}
	}
	return validate(sch, v, schemaKind)
}

// loadSchemas reads an operation's checked-in input and output schemas.
func loadSchemas(name string) (input, output json.RawMessage, err error) {
	in, out, err := schemas.Operation(name)
	if err != nil {
		return nil, nil, fmt.Errorf("operation %q has no checked-in schema: %w", name, err)
	}
	return in, out, nil
}
