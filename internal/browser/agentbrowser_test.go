package browser

import (
	"encoding/binary"
	"encoding/json"
	"testing"
	"unicode/utf16"
)

func TestPSQuoteEscapesSingleQuotes(t *testing.T) {
	got := psQuote("a'b")
	want := "'a''b'"
	if got != want {
		t.Errorf("psQuote = %q, want %q", got, want)
	}
}

func TestPSJoin(t *testing.T) {
	got := psJoin([]string{"open", "about:blank"})
	want := "'open' 'about:blank'"
	if got != want {
		t.Errorf("psJoin = %q, want %q", got, want)
	}
}

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`{"success":true}`:                  `{"success":true}`,
		"warning\r\n{\"success\":true}\r\n": `{"success":true}`,
		"junk {\"a\":{\"b\":1}} tail":       `{"a":{"b":1}}`,
		"no json here":                      ``,
	}
	for in, want := range cases {
		got := string(extractJSON([]byte(in)))
		if got != want {
			t.Errorf("extractJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsCompleteResponse(t *testing.T) {
	if !isCompleteResponse([]byte(`{"success":true,"data":null,"error":null}`)) {
		t.Error("valid envelope not recognized")
	}
	if isCompleteResponse([]byte(`{"success":tr`)) {
		t.Error("partial envelope recognized")
	}
	if isCompleteResponse([]byte("CMD.EXE was started with the above path")) {
		t.Error("non-JSON recognized as complete")
	}
}

func TestDecodeOutputUTF16LE(t *testing.T) {
	body := `{"success":true}`
	u := utf16.Encode([]rune(body))
	buf := []byte{0xFF, 0xFE}
	for _, r := range u {
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], r)
		buf = append(buf, b[:]...)
	}
	if got := string(decodeOutput(buf)); got != body {
		t.Errorf("decodeOutput = %q, want %q", got, body)
	}
}

func TestDecodeOutputUTF8BOM(t *testing.T) {
	buf := append([]byte{0xEF, 0xBB, 0xBF}, []byte("hello")...)
	if got := string(decodeOutput(buf)); got != "hello" {
		t.Errorf("decodeOutput = %q, want hello", got)
	}
}

func TestToWindowsPath(t *testing.T) {
	got, err := toWindowsPath("/mnt/c/Users/x/shot.png")
	if err != nil {
		t.Fatal(err)
	}
	if got != `C:\Users\x\shot.png` {
		t.Errorf("toWindowsPath = %q", got)
	}
	if _, err := toWindowsPath("/home/x/shot.png"); err == nil {
		t.Error("non-mount path should error")
	}
}

func TestEvalExtractsResult(t *testing.T) {
	// Mirrors the agent-browser envelope for `eval`.
	raw := []byte(`{"success":true,"data":{"result":42,"origin":"about:blank"},"error":null}`)
	env, err := parseEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	if string(data.Result) != "42" {
		t.Errorf("result = %s, want 42", data.Result)
	}
}

func TestParseEnvelopeError(t *testing.T) {
	raw := []byte(`{"success":false,"data":null,"error":"boom"}`)
	if _, err := parseEnvelope(raw); err == nil {
		t.Fatal("expected error for success:false")
	}
}
