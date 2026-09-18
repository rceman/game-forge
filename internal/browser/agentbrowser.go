package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/rceman/game-forge/internal/config"
)

// AgentBrowser is a Provider backed by the agent-browser CLI.
//
// It is deliberately the only place in Game Forge that knows agent-browser
// exists. When the provider host is Windows and Game Forge is not itself
// running on Windows (the WSL case), commands are relayed through Windows
// PowerShell interop.
type AgentBrowser struct {
	cli       string
	chrome    string
	headless  bool
	unmuted   bool
	namespace string
	host      string

	psExe   string        // PowerShell executable (interop or native)
	timeout time.Duration // per-invocation ceiling
	tmpDir  string        // cached writable Windows temp dir (WSL path)
}

// NewAgentBrowser builds an agent-browser provider from machine configuration.
func NewAgentBrowser(cfg *config.Config, namespace string) *AgentBrowser {
	ab := cfg.Browser.AgentBrowser
	return &AgentBrowser{
		cli:       ab.CLI,
		chrome:    ab.Chrome,
		headless:  ab.Headless,
		unmuted:   ab.Unmuted,
		namespace: namespace,
		host:      cfg.Browser.Host,
		psExe:     powershellPath(),
	}
}

// Name implements Provider.
func (a *AgentBrowser) Name() string { return "agent-browser" }

// Namespace returns the provider namespace used for this session.
func (a *AgentBrowser) Namespace() string { return a.namespace }

// useInterop reports whether commands must be relayed to a Windows host.
func (a *AgentBrowser) useInterop() bool {
	return strings.EqualFold(a.host, "windows") && runtime.GOOS != "windows"
}

// resolveCLI ensures a usable CLI path, discovering one via the host when the
// configuration leaves it empty.
func (a *AgentBrowser) resolveCLI(ctx context.Context) (string, error) {
	if a.cli != "" {
		return a.cli, nil
	}
	if !a.useInterop() {
		if p, err := exec.LookPath("agent-browser"); err == nil {
			a.cli = p
			return p, nil
		}
		return "", fmt.Errorf("agent-browser not found on PATH")
	}
	// Ask the Windows host where agent-browser lives.
	out, err := exec.CommandContext(ctx, a.psExe, "-NoProfile", "-Command",
		"(Get-Command agent-browser -ErrorAction SilentlyContinue).Source").Output()
	if err != nil {
		return "", fmt.Errorf("resolve agent-browser on windows host: %w", err)
	}
	p := strings.TrimSpace(strings.ReplaceAll(string(out), "\r", ""))
	if p == "" {
		return "", fmt.Errorf("agent-browser not found on the windows host")
	}
	a.cli = p
	return p, nil
}

// baseArgs returns the flags common to every invocation.
//
// The managed Chrome launch flags are repeated on every call, not just on the
// launching one. agent-browser treats --args as a GLOBAL option (it must precede
// the subcommand, and a copy after the subcommand is silently ignored) and the
// daemon relaunches Chrome with its default flags whenever a later invocation
// omits them. Supplying them once and then calling `set viewport` was enough to
// lose audio suppression entirely.
func (a *AgentBrowser) baseArgs() []string {
	args := []string{"--namespace", a.namespace}
	if a.chrome != "" {
		args = append(args, "--executable-path", a.chrome)
	}
	if la := a.LaunchArgs(); la != "" {
		args = append(args, "--args", la)
	}
	if !a.headless {
		// agent-browser defaults to headless; --headed is the explicit opposite.
		args = append(args, "--headed")
	}
	return args
}

// Check validates the provider and performs a bounded open/close round trip.
func (a *AgentBrowser) Check(ctx context.Context) (*CheckResult, error) {
	res := &CheckResult{Provider: a.Name(), Host: a.hostOrDefault()}

	cli, err := a.resolveCLI(ctx)
	if err != nil {
		res.AddProblem("%v", err)
		return res, nil
	}
	res.AddDetail("cli: %s", cli)

	if v, err := a.version(ctx); err != nil {
		res.AddProblem("version check failed: %v", err)
	} else {
		res.Version = v
		res.AddDetail("version: %s", v)
	}

	if a.chrome != "" {
		res.Chrome = a.chrome
		res.AddDetail("chrome: %s", a.chrome)
	}

	// Bounded open/close proves the whole path, not just that a binary exists.
	start := nowMS()
	if err := a.Open(ctx, "about:blank"); err != nil {
		res.AddProblem("open about:blank failed: %v", err)
	} else {
		res.LaunchMS = nowMS() - start
		res.AddDetail("bounded open: about:blank in %dms", res.LaunchMS)
		if _, err := a.Eval(ctx, "1+1"); err != nil {
			res.AddProblem("eval probe failed: %v", err)
		} else {
			res.AddDetail("eval probe: ok")
		}
		if err := a.Close(ctx); err != nil {
			res.AddProblem("close failed: %v", err)
		} else {
			res.AddDetail("close: ok")
		}
	}

	res.OK = len(res.Problems) == 0
	res.AddDetail("launch args: %s", a.LaunchArgs())
	return res, nil
}

// version returns the agent-browser version string.
func (a *AgentBrowser) version(ctx context.Context) (string, error) {
	out, err := a.runRaw(ctx, []string{"--version"})
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(out))
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		return fields[len(fields)-1], nil
	}
	return line, nil
}

// Open implements Provider.
func (a *AgentBrowser) Open(ctx context.Context, url string) error {
	return a.open(ctx, url)
}

// Navigate implements Provider.
func (a *AgentBrowser) Navigate(ctx context.Context, url string) error {
	return a.open(ctx, url)
}

// open launches the browser with Game Forge's managed flags and navigates.
func (a *AgentBrowser) open(ctx context.Context, url string) error {
	args := []string{"open"}
	if url != "" {
		args = append(args, url)
	}
	_, err := a.run(ctx, args)
	return err
}

// LaunchArgs returns the comma-separated Chrome flags Game Forge applies to
// every managed browser launch.
//
// The list MUST stay comma-free: agent-browser splits --args on commas, so a
// value like --disable-features=A,B would silently shred into two arguments.
//
// Automated sessions are silent by default. --mute-audio suppresses physical
// audio output only: the page's AudioContext stays active, cue generation and
// rate limiting keep running, and every audio counter remains testable. Game
// Forge never touches the project's own mute state.
func (a *AgentBrowser) LaunchArgs() string {
	flags := []string{
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--disable-background-networking",
		"--disable-sync",
		"--disable-default-apps",
		"--disable-translate",
		"--disable-component-update",
		"--no-first-run",
	}
	if !a.unmuted {
		flags = append(flags, "--mute-audio")
	}
	return strings.Join(flags, ",")
}

// SetUnmuted toggles browser audio-output suppression. It exists for the
// explicit diagnostic override; normal automation leaves it off.
func (a *AgentBrowser) SetUnmuted(v bool) { a.unmuted = v }

// Reload implements Provider.
func (a *AgentBrowser) Reload(ctx context.Context) error {
	_, err := a.run(ctx, []string{"reload"})
	return err
}

// SetViewport implements Provider.
func (a *AgentBrowser) SetViewport(ctx context.Context, width, height int) error {
	_, err := a.run(ctx, []string{"set", "viewport", strconv.Itoa(width), strconv.Itoa(height)})
	return err
}

// Click implements Provider.
func (a *AgentBrowser) Click(ctx context.Context, selector string) error {
	_, err := a.run(ctx, []string{"click", selector})
	return err
}

// Press implements Provider.
func (a *AgentBrowser) Press(ctx context.Context, key string) error {
	_, err := a.run(ctx, []string{"press", key})
	return err
}

// PageErrors implements Provider.
func (a *AgentBrowser) PageErrors(ctx context.Context) ([]string, error) {
	return a.messages(ctx, "errors")
}

// ConsoleErrors implements Provider.
//
// agent-browser reports every console entry; only error-level entries are
// failures. debug/info/warn entries (dev-server chatter, HMR notices) are not.
func (a *AgentBrowser) ConsoleErrors(ctx context.Context) ([]string, error) {
	raw, err := a.run(ctx, []string{"console"})
	if err != nil {
		return nil, err
	}
	env, err := parseEnvelope(raw)
	if err != nil {
		return nil, err
	}
	if msgs, ok := typedConsoleErrors(env.Data); ok {
		return msgs, nil
	}
	var out []string
	for _, m := range extractMessages(env.Data) {
		if strings.Contains(strings.ToLower(m), "error") {
			out = append(out, m)
		}
	}
	return out, nil
}

// typedConsoleErrors returns error-level console messages when the payload
// carries per-entry types.
func typedConsoleErrors(data json.RawMessage) ([]string, bool) {
	var obj struct {
		Messages []struct {
			Text string `json:"text"`
			Type string `json:"type"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &obj); err != nil || obj.Messages == nil {
		return nil, false
	}
	out := []string{}
	for _, m := range obj.Messages {
		if strings.EqualFold(m.Type, "error") || strings.EqualFold(m.Type, "assert") {
			out = append(out, m.Text)
		}
	}
	return out, true
}

// ClearErrors implements Provider.
func (a *AgentBrowser) ClearErrors(ctx context.Context) error {
	if _, err := a.run(ctx, []string{"errors", "--clear"}); err != nil {
		return err
	}
	_, err := a.run(ctx, []string{"console", "--clear"})
	return err
}

// messages reads an agent-browser diagnostics stream (errors or console).
func (a *AgentBrowser) messages(ctx context.Context, kind string) ([]string, error) {
	raw, err := a.run(ctx, []string{kind})
	if err != nil {
		return nil, err
	}
	env, err := parseEnvelope(raw)
	if err != nil {
		return nil, err
	}
	return extractMessages(env.Data), nil
}

// Renderer implements Provider.
func (a *AgentBrowser) Renderer(ctx context.Context) (*RendererInfo, error) {
	// Single line on purpose: multi-line arguments do not survive the
	// PowerShell interop boundary intact.
	js := `(() => { const c = document.createElement('canvas'); const gl = c.getContext('webgl2') || c.getContext('webgl'); if (!gl) return JSON.stringify({ renderer: 'none' }); const dbg = gl.getExtension('WEBGL_debug_renderer_info'); const o = { vendor: gl.getParameter(gl.VENDOR), renderer: gl.getParameter(gl.RENDERER) }; if (dbg) { o.unmaskedVendor = gl.getParameter(dbg.UNMASKED_VENDOR_WEBGL); o.unmaskedRenderer = gl.getParameter(dbg.UNMASKED_RENDERER_WEBGL); } return JSON.stringify(o); })()`
	raw, err := a.Eval(ctx, js)
	if err != nil {
		return nil, err
	}
	var info RendererInfo
	if err := json.Unmarshal(unwrapJSONString(raw), &info); err != nil {
		return nil, fmt.Errorf("parse renderer info: %w (%.200s)", err, raw)
	}
	return &info, nil
}

// Eval implements Provider.
func (a *AgentBrowser) Eval(ctx context.Context, js string) (json.RawMessage, error) {
	raw, err := a.run(ctx, []string{"eval", js})
	if err != nil {
		return nil, err
	}
	env, err := parseEnvelope(raw)
	if err != nil {
		return nil, err
	}
	var data struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return env.Data, nil
	}
	if len(data.Result) == 0 {
		return env.Data, nil
	}
	return data.Result, nil
}

// Screenshot implements Provider.
//
// The Windows browser writes the PNG, so when Game Forge runs under WSL the
// target must be on a mounted drive. A target anywhere else (e.g. /tmp) is
// captured to a Windows temp file and copied back, so callers can name any
// path.
func (a *AgentBrowser) Screenshot(ctx context.Context, path string) error {
	out := path
	var tempLocal string
	if a.useInterop() {
		if w, err := toWindowsPath(path); err == nil {
			out = w
		} else {
			dir, err := a.screenshotTempDir(ctx)
			if err != nil {
				return err
			}
			f, err := os.CreateTemp(dir, "game-forge-shot-*.png")
			if err != nil {
				return fmt.Errorf("create screenshot temp: %w", err)
			}
			tempLocal = f.Name()
			f.Close()
			w, err := toWindowsPath(tempLocal)
			if err != nil {
				return err
			}
			out = w
		}
	}
	if _, err := a.run(ctx, []string{"screenshot", out}); err != nil {
		return err
	}
	if tempLocal != "" {
		if err := copyFile(tempLocal, path); err != nil {
			return err
		}
		_ = os.Remove(tempLocal)
	}
	return nil
}

// screenshotTempDir resolves a writable Windows temp directory as a WSL path.
func (a *AgentBrowser) screenshotTempDir(ctx context.Context) (string, error) {
	if a.tmpDir != "" {
		return a.tmpDir, nil
	}
	out, err := exec.CommandContext(ctx, a.psExe, "-NoProfile", "-Command", "Write-Output $env:TEMP").Output()
	if err != nil {
		return "", fmt.Errorf("resolve windows temp dir: %w", err)
	}
	winTemp := strings.TrimSpace(strings.ReplaceAll(string(out), "\r", ""))
	wsl, err := fromWindowsPath(winTemp)
	if err != nil {
		return "", err
	}
	a.tmpDir = wsl
	return wsl, nil
}

// copyFile copies src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy to %s: %w", dst, err)
	}
	return nil
}

// Close implements Provider.
func (a *AgentBrowser) Close(ctx context.Context) error {
	_, err := a.run(ctx, []string{"close", "--all"})
	return err
}

// run invokes the CLI with the standard session flags and returns decoded
// output.
func (a *AgentBrowser) run(ctx context.Context, args []string) ([]byte, error) {
	full := append(a.baseArgs(), args...)
	full = append(full, "--json")
	return a.runRaw(ctx, full)
}

// runRaw invokes the CLI with exactly args and returns decoded output.
//
// Output is captured through a file, never a pipe. The agent-browser daemon
// outlives the CLI and inherits its stdout: a pipe never reaches EOF, and the
// launcher blocks waiting on the daemon it just started. We therefore start the
// command, watch the output file, and stop waiting as soon as a complete
// response has been written. The daemon is detached and survives.
func (a *AgentBrowser) runRaw(ctx context.Context, args []string) ([]byte, error) {
	cli, err := a.resolveCLI(ctx)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp("", "game-forge-provider-*.json")
	if err != nil {
		return nil, fmt.Errorf("create result file: %w", err)
	}
	path := f.Name()
	defer os.Remove(path)
	defer f.Close()

	var cmd *exec.Cmd
	if a.useInterop() {
		cmdStr := "& " + psQuote(cli) + " " + psJoin(args)
		cmd = exec.Command(a.psExe, "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", cmdStr)
		// A Windows-hosted launcher cannot run from a \\wsl.localhost UNC
		// working directory, so pin it to a local Windows directory.
		cmd.Dir = interopWorkDir()
	} else {
		cmd = exec.Command(cli, args...)
	}
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start agent-browser: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timeout := a.commandTimeout()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case werr := <-done:
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil, fmt.Errorf("read provider output: %w", rerr)
			}
			data = decodeOutput(data)
			if werr != nil && !isCompleteResponse(data) {
				return nil, fmt.Errorf("agent-browser %s: %w", strings.Join(args, " "), werr)
			}
			return data, nil
		case <-tick.C:
			if data, ok := readCompleteResponse(path); ok {
				// The response is ready. The launcher may still be waiting on
				// the daemon it started; stop it so we do not block.
				_ = cmd.Process.Kill()
				<-done
				return data, nil
			}
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-done
			return nil, ctx.Err()
		case <-deadline.C:
			_ = cmd.Process.Kill()
			<-done
			return nil, fmt.Errorf("agent-browser %s: timed out after %s", strings.Join(args, " "), timeout)
		}
	}
}

// commandTimeout is the per-invocation ceiling for a single CLI call.
func (a *AgentBrowser) commandTimeout() time.Duration {
	if a.timeout > 0 {
		return a.timeout
	}
	return 90 * time.Second
}

// readCompleteResponse reads path and reports whether it holds a complete
// provider response, tolerating a partially written file.
func readCompleteResponse(path string) ([]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	data = decodeOutput(data)
	if !isCompleteResponse(data) {
		return nil, false
	}
	return data, true
}

// isCompleteResponse reports whether data is a parseable provider envelope.
func isCompleteResponse(data []byte) bool {
	j := extractJSON(data)
	if len(j) == 0 {
		return false
	}
	var env envelope
	return json.Unmarshal(j, &env) == nil
}

// extractJSON returns the JSON object embedded in data, tolerating stray
// launcher warnings around it.
func extractJSON(data []byte) []byte {
	s := bytes.TrimSpace(data)
	i := bytes.IndexByte(s, '{')
	j := bytes.LastIndexByte(s, '}')
	if i < 0 || j <= i {
		return nil
	}
	return s[i : j+1]
}

// hostOrDefault normalizes the host label for reporting.
func (a *AgentBrowser) hostOrDefault() string {
	if a.host == "" {
		if runtime.GOOS == "windows" {
			return "windows"
		}
		return "linux"
	}
	return a.host
}

// parseEnvelope validates the agent-browser JSON envelope.
func parseEnvelope(raw []byte) (*envelope, error) {
	j := extractJSON(raw)
	if len(j) == 0 {
		return nil, fmt.Errorf("no provider response found (%.200s)", raw)
	}
	env := &envelope{}
	if err := json.Unmarshal(j, env); err != nil {
		return nil, fmt.Errorf("parse provider response: %w (%.200s)", err, j)
	}
	if !env.Success {
		if env.Error != nil && *env.Error != "" {
			return nil, fmt.Errorf("provider error: %s", *env.Error)
		}
		return nil, fmt.Errorf("provider reported failure")
	}
	return env, nil
}

type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *string         `json:"error"`
}

// powershellPath locates a PowerShell executable for the current platform.
func powershellPath() string {
	if runtime.GOOS == "windows" {
		return "powershell.exe"
	}
	for _, c := range []string{
		"/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe",
		"/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell",
	} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "powershell.exe"
}

// interopWorkDir returns a working directory on a mounted Windows drive for
// interop launches. Windows launchers cannot run from a \\wsl.localhost UNC
// path, so Game Forge pins them to a mounted drive root (a local Windows
// directory on the other side of the interop boundary).
func interopWorkDir() string {
	for _, d := range []string{"c", "d", "e"} {
		p := "/mnt/" + d
		if _, err := os.Stat(p); err == nil {
			return p + "/"
		}
	}
	return "/"
}

// decodeOutput decodes provider output, which PowerShell redirection writes as
// UTF-16LE.
func decodeOutput(b []byte) []byte {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		u := make([]uint16, (len(b)-2)/2)
		for i := range u {
			u[i] = uint16(b[2+2*i]) | uint16(b[2+2*i+1])<<8
		}
		return []byte(string(utf16.Decode(u)))
	}
	return bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
}

// psQuote single-quotes a value for PowerShell, doubling embedded quotes.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// psJoin quotes an argument list for PowerShell.
//
// Windows PowerShell 5.1 does not escape embedded double quotes when passing
// arguments to a native command: it wraps the value in double quotes and lets
// the inner ones terminate the string early. Game Forge therefore escapes
// embedded double quotes itself so the native argument parser (MSVCRT) turns
// them back into literal quotes. This matters for `eval`, whose JavaScript is
// full of quotes.
func psJoin(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		escaped := strings.ReplaceAll(a, `"`, `\"`)
		parts[i] = psQuote(escaped)
	}
	return strings.Join(parts, " ")
}

// toWindowsPath converts a WSL path (/mnt/<drive>/...) to a Windows path.
func toWindowsPath(p string) (string, error) {
	clean := filepath.Clean(p)
	if !strings.HasPrefix(clean, "/mnt/") {
		return "", fmt.Errorf("path %q is not on a mounted windows drive", p)
	}
	rest := strings.TrimPrefix(clean, "/mnt/")
	drive, tail, ok := strings.Cut(rest, "/")
	if !ok || len(drive) != 1 {
		return "", fmt.Errorf("path %q is not on a mounted windows drive", p)
	}
	return strings.ToUpper(drive) + `:\` + strings.ReplaceAll(tail, "/", `\`), nil
}

// fromWindowsPath converts a Windows path (C:\...) to a WSL mount path.
func fromWindowsPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if len(p) < 3 || p[1] != ':' || (p[2] != '\\' && p[2] != '/') {
		return "", fmt.Errorf("path %q is not a windows drive path", p)
	}
	drive := strings.ToLower(string(p[0]))
	tail := strings.ReplaceAll(p[3:], `\`, "/")
	return "/mnt/" + drive + "/" + tail, nil
}

// nowMS returns the current time in milliseconds since the epoch.
func nowMS() int64 { return time.Now().UnixMilli() }

// unwrapJSONString returns the raw bytes, unwrapping a JSON string literal.
func unwrapJSONString(raw json.RawMessage) json.RawMessage {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return json.RawMessage(s)
	}
	return raw
}

// extractMessages pulls a list of human-readable messages out of an
// agent-browser diagnostics payload, tolerating several plausible shapes.
func extractMessages(data json.RawMessage) []string {
	if len(data) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) == nil {
		for _, key := range []string{
			"errors", "pageErrors", "console", "messages", "logs", "entries", "items", "events",
		} {
			if v, ok := obj[key]; ok {
				if msgs := stringsFromJSON(v); msgs != nil {
					return msgs
				}
			}
		}
		for _, v := range obj {
			if msgs := stringsFromJSON(v); msgs != nil {
				return msgs
			}
		}
	}
	if msgs := stringsFromJSON(data); msgs != nil {
		return msgs
	}
	return splitNonEmptyLines(string(unwrapJSONString(data)))
}

// stringsFromJSON extracts a string slice from an array of strings or objects.
func stringsFromJSON(raw json.RawMessage) []string {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		var s string
		if json.Unmarshal(item, &s) == nil {
			out = append(out, s)
			continue
		}
		var obj map[string]any
		if json.Unmarshal(item, &obj) == nil {
			for _, key := range []string{"message", "text", "msg", "description", "value"} {
				if v, ok := obj[key].(string); ok {
					out = append(out, v)
					break
				}
			}
		}
	}
	return out
}

// splitNonEmptyLines splits text into non-blank, trimmed lines.
func splitNonEmptyLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.Trim(line, "\""))
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
