package op_test

import (
	"encoding/json"
	"testing"

	"github.com/rceman/game-forge/internal/op"
)

func mustDecode(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("bad test JSON: %v", err)
	}
	return v
}

// TestRequestEnvelopeValidation covers the request schema.
func TestRequestEnvelopeValidation(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool // true = valid
	}{
		{"minimal", `{"v":1,"op":"scenario.run"}`, true},
		{"full", `{"v":1,"id":"42","op":"scenario.run","cwd":"/x","args":{"id":"a"}}`, true},
		{"missing op", `{"v":1}`, false},
		{"missing v", `{"op":"scenario.run"}`, false},
		{"v not integer", `{"v":"1","op":"x"}`, false},
		{"extra field", `{"v":1,"op":"x","bogus":1}`, false},
		{"op wrong type", `{"v":1,"op":3}`, false},
	}
	for _, c := range cases {
		err := op.ValidateRequest(mustDecode(t, c.raw))
		if c.want && err != nil {
			t.Errorf("%s: expected valid, got %v", c.name, err)
		}
		if !c.want && err == nil {
			t.Errorf("%s: expected invalid, passed", c.name)
		}
	}
}

// TestEventValidation covers the streamed event schema.
func TestEventValidation(t *testing.T) {
	good := []string{
		`{"ev":"start","run":"r1"}`,
		`{"ev":"stage","name":"check","status":"pass","ms":1180}`,
		`{"ev":"artifact","kind":"image","ref":"a://1","path":"/x"}`,
		`{"ev":"done","status":"pass","code":0}`,
		`{"ev":"done","status":"fail","code":1,"err":{"code":"failed","msg":"x"}}`,
	}
	for _, raw := range good {
		if err := op.ValidateEvent(mustDecode(t, raw)); err != nil {
			t.Errorf("expected valid event %s, got %v", raw, err)
		}
	}
	bad := []string{
		`{"ev":"bogus"}`,
		`{"ev":"stage"}`,         // stage needs name+status
		`{"status":"pass"}`,      // ev required
		`{"ev":"done","code":2}`, // code only 0|1
		`{"ev":"done","status":"x"}`,
	}
	for _, raw := range bad {
		if err := op.ValidateEvent(mustDecode(t, raw)); err == nil {
			t.Errorf("expected invalid event %s, passed", raw)
		}
	}
}

// TestErrorValidation covers the structured error schema.
func TestErrorValidation(t *testing.T) {
	if err := op.ValidateError(map[string]any{"code": "failed", "msg": "x"}); err != nil {
		t.Errorf("valid error rejected: %v", err)
	}
	if err := op.ValidateError(map[string]any{"msg": "no code"}); err == nil {
		t.Error("error without code should be invalid")
	}
}

// TestResponseValidation covers the reply envelope.
func TestResponseValidation(t *testing.T) {
	if err := op.ValidateResponse(op.Response{OK: true, Data: map[string]any{"a": 1}}); err != nil {
		t.Errorf("valid response rejected: %v", err)
	}
	if err := op.ValidateResponse(op.Response{OK: false, Err: &op.Error{Code: "failed", Msg: "x"}}); err != nil {
		t.Errorf("valid error response rejected: %v", err)
	}
}

// TestOperationArgsValidation proves an op's input schema gates its args.
func TestOperationArgsValidation(t *testing.T) {
	reg := buildRegistry(t)
	o, ok := reg.Lookup("scenario.run")
	if !ok {
		t.Fatal("scenario.run not registered")
	}
	// Missing required "id" must fail.
	if err := o.ValidateArgs(json.RawMessage(`{}`)); err == nil {
		t.Error("args missing id should fail")
	}
	// Negative ticks must fail (schema minimum 0).
	if err := o.ValidateArgs(json.RawMessage(`{"id":"x","ticks":-1}`)); err == nil {
		t.Error("negative ticks should fail")
	}
	// Unknown property must fail.
	if err := o.ValidateArgs(json.RawMessage(`{"id":"x","bogus":1}`)); err == nil {
		t.Error("unknown property should fail")
	}
	// Valid args pass.
	if err := o.ValidateArgs(json.RawMessage(`{"id":"x","ticks":600,"browser":true}`)); err != nil {
		t.Errorf("valid args rejected: %v", err)
	}
}

// TestOperationOutputValidation proves schema-invalid Core output is rejected
// as an internal contract error, not sent to the client.
func TestOperationOutputValidation(t *testing.T) {
	reg := buildRegistry(t)
	o, _ := reg.Lookup("resource.list")
	// Valid output shape passes.
	if err := o.ValidateOutput(map[string]any{"resources": []any{}}); err != nil {
		t.Errorf("valid output rejected: %v", err)
	}
	// Missing the required "resources" field must be an internal error.
	err := o.ValidateOutput(map[string]any{"nope": true})
	if err == nil {
		t.Error("schema-invalid output should be rejected")
	} else if err.Code != op.CodeInvalidOutput {
		t.Errorf("expected invalid_output, got %s", err.Code)
	}
}
