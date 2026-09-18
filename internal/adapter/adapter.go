// Package adapter speaks the Game Forge JSONL-over-stdio protocol.
//
// Game Forge drives a project's headless runtime through a child process: one
// JSON request per line in, one JSON response per line out. No daemon, no
// network port, no service lifecycle. The protocol is language-agnostic; the
// only thing Game Forge knows about the project is the declared command.
package adapter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// Client is a running adapter process.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	mu     sync.Mutex
	seq    int
	broken bool
}

// Start launches the adapter command in dir.
func Start(dir string, command []string, stderr io.Writer) (*Client, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("adapter command is empty")
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = dir
	if stderr == nil {
		stderr = os.Stderr
	}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("adapter stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("adapter stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start adapter: %w", err)
	}
	return &Client{cmd: cmd, stdin: stdin, stdout: bufio.NewReaderSize(stdout, 1<<20)}, nil
}

// response is the JSONL response envelope.
type response struct {
	ID     json.RawMessage `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *string         `json:"error"`
}

// Call sends one request and returns the decoded result.
//
// Calls are serialized: the protocol is a single request/response stream.
func (c *Client) Call(ctx context.Context, op string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken {
		return nil, fmt.Errorf("adapter connection is broken")
	}
	c.seq++
	req := map[string]any{"id": c.seq, "op": op, "params": params}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		c.broken = true
		return nil, fmt.Errorf("write request: %w", err)
	}
	line, err := c.readLine(ctx)
	if err != nil {
		c.broken = true
		return nil, err
	}
	res := &response{}
	if err := json.Unmarshal(line, res); err != nil {
		return nil, fmt.Errorf("parse response: %w (%.200s)", err, line)
	}
	if !res.OK {
		msg := "adapter reported failure"
		if res.Error != nil && *res.Error != "" {
			msg = *res.Error
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return res.Result, nil
}

// readLine reads one response line, honouring the context deadline.
func (c *Client) readLine(ctx context.Context) ([]byte, error) {
	type result struct {
		line []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := c.stdout.ReadBytes('\n')
		ch <- result{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil && len(r.line) == 0 {
			return nil, fmt.Errorf("read response: %w", r.err)
		}
		return r.line, nil
	case <-ctx.Done():
		_ = c.cmd.Process.Kill()
		return nil, ctx.Err()
	}
}

// Close terminates the adapter process.
func (c *Client) Close() error {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return c.cmd.Wait()
}

// Capabilities is the adapter's declared capability response.
type Capabilities struct {
	Contract string   `json:"contract"`
	Ops      []string `json:"ops"`
}

// Capabilities queries the adapter's contract and supported operations.
func (c *Client) Capabilities(ctx context.Context) (*Capabilities, error) {
	raw, err := c.Call(ctx, "capabilities", map[string]any{})
	if err != nil {
		return nil, err
	}
	caps := &Capabilities{}
	if err := json.Unmarshal(raw, caps); err != nil {
		return nil, fmt.Errorf("parse capabilities: %w", err)
	}
	return caps, nil
}
