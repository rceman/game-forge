package browser

import (
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/rceman/game-forge/internal/config"
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

func TestLaunchArgsSuppressAudioByDefault(t *testing.T) {
	p := NewAgentBrowser(config.Default(), "ns")
	args := p.LaunchArgs()
	if !strings.Contains(args, "--mute-audio") {
		t.Fatalf("LaunchArgs must suppress audio output by default, got %q", args)
	}
	// agent-browser splits --args on commas, so no flag may contain one.
	for _, f := range strings.Split(args, ",") {
		if strings.TrimSpace(f) == "" {
			t.Fatalf("LaunchArgs has an empty flag: %q", args)
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(args), "--") {
		t.Fatalf("LaunchArgs must start with a flag: %q", args)
	}
}

func TestLaunchArgsUnmutedOverride(t *testing.T) {
	p := NewAgentBrowser(config.Default(), "ns")
	p.SetUnmuted(true)
	if strings.Contains(p.LaunchArgs(), "--mute-audio") {
		t.Fatalf("unmuted override must not pass --mute-audio, got %q", p.LaunchArgs())
	}
}

// TestBaseArgsRepeatLaunchFlags guards a real regression: agent-browser relaunches
// Chrome with its default flags whenever an invocation omits --args, so the
// flags must be repeated on every call, not just on the launching one.
func TestBaseArgsRepeatLaunchFlags(t *testing.T) {
	p := NewAgentBrowser(config.Default(), "ns")
	base := p.baseArgs()
	if !containsSeq(base, "--args", p.LaunchArgs()) {
		t.Fatalf("baseArgs must carry --args on every invocation, got %q", base)
	}
	if idxOf(base, "--args") > idxOf(base, "open") && idxOf(base, "open") >= 0 {
		t.Fatalf("--args is a global option and must precede the subcommand, got %q", base)
	}
	p.SetUnmuted(true)
	if strings.Contains(p.LaunchArgs(), "--mute-audio") {
		t.Fatalf("unmuted provider must not suppress audio, got %q", p.baseArgs())
	}
}

func containsSeq(args []string, want ...string) bool {
	at := idxOf(args, want[0])
	if at < 0 {
		return false
	}
	for i, w := range want {
		if at+i >= len(args) || args[at+i] != w {
			return false
		}
	}
	return true
}

func idxOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

func TestConsoleErrorsOnlyErrorLevel(t *testing.T) {
	data := json.RawMessage(`{"messages":[
		{"text":"[vite] connecting...","type":"debug"},
		{"text":"boom","type":"error"},
		{"text":"careful","type":"warn"}
	]}`)
	msgs, ok := typedConsoleErrors(data)
	if !ok {
		t.Fatal("typedConsoleErrors should recognise the messages shape")
	}
	if len(msgs) != 1 || msgs[0] != "boom" {
		t.Fatalf("typedConsoleErrors = %v, want [boom]", msgs)
	}
}
