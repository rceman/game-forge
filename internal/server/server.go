// Package server owns the lifecycle of project dev/production servers.
//
// Game Forge starts a server in its own process group, waits for a declared
// URL to become ready, registers it as an owned resource, and stops exactly
// that process group. It never kills processes by executable name.
package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rceman/game-forge/internal/process"
)

// Options describe a server Game Forge may own.
type Options struct {
	// Name is "dev" or "prod".
	Name string
	// Command starts the server.
	Command []string
	// URL is polled for readiness.
	URL string
	// ReadyTimeout bounds the readiness wait.
	ReadyTimeout time.Duration
	// Dir is the project root the server runs in.
	Dir string
	// LogDir receives the server's combined output.
	LogDir string
	// RunID groups resources owned by one invocation.
	RunID string
	// Project is the owning project id.
	Project string
	// Lease is the resource lease. Zero means no lease.
	Lease time.Duration
	// Registry receives the owned-resource record. Optional.
	Registry *process.Registry
	// Stderr receives a copy of the server output. Optional.
	Stderr *os.File
}

// Server is a running (possibly reused) server.
type Server struct {
	name    string
	url     string
	cmd     *exec.Cmd
	owned   bool
	id      string
	reg     *process.Registry
	logPath string
}

// URL returns the server's base URL.
func (s *Server) URL() string { return s.url }

// Owned reports whether Game Forge started this server.
func (s *Server) Owned() bool { return s.owned }

// LogPath returns the path to the server log.
func (s *Server) LogPath() string { return s.logPath }

// PID returns the process id of an owned server, or 0 when reused.
func (s *Server) PID() int {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// Ensure returns a server for opts.URL, starting one only if the URL is not
// already reachable. A reused server is not owned and will not be stopped.
func Ensure(ctx context.Context, opts Options) (*Server, error) {
	if opts.URL == "" {
		return nil, fmt.Errorf("server %q: url is required", opts.Name)
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 60 * time.Second
	}
	if Reachable(ctx, opts.URL, 1500*time.Millisecond) {
		return &Server{name: opts.Name, url: opts.URL, owned: false}, nil
	}
	if len(opts.Command) == 0 {
		return nil, fmt.Errorf("server %q: command is required to start it", opts.Name)
	}

	if opts.LogDir != "" {
		_ = os.MkdirAll(opts.LogDir, 0o755)
	}
	logPath := filepath.Join(opts.LogDir, "server-"+opts.Name+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("create server log: %w", err)
	}

	cmd := exec.Command(opts.Command[0], opts.Command[1:]...)
	cmd.Dir = opts.Dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("start server %q: %w", opts.Name, err)
	}

	srv := &Server{name: opts.Name, url: opts.URL, cmd: cmd, owned: true, reg: opts.Registry, logPath: logPath}

	deadline := time.Now().Add(opts.ReadyTimeout)
	for time.Now().Before(deadline) {
		if Reachable(ctx, opts.URL, 1000*time.Millisecond) {
			srv.register(opts)
			return srv, nil
		}
		if cmd.ProcessState != nil {
			break
		}
		select {
		case <-ctx.Done():
			srv.Stop()
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	srv.Stop()
	tail := tailFile(logPath, 15)
	return nil, fmt.Errorf("server %q did not become ready at %s within %s\n%s", opts.Name, opts.URL, opts.ReadyTimeout, tail)
}

// register records the server as an owned resource.
func (s *Server) register(opts Options) {
	if s.reg == nil {
		return
	}
	s.id = fmt.Sprintf("server-%s-%d", opts.Name, s.cmd.Process.Pid)
	res := &process.Resource{
		ID:       s.id,
		RunID:    opts.RunID,
		Project:  opts.Project,
		Kind:     process.KindServer,
		Provider: "local",
		PID:      s.cmd.Process.Pid,
		Metadata: map[string]string{"url": opts.URL, "name": opts.Name},
	}
	if pgid := processGroupID(s.cmd); pgid > 0 {
		res.Metadata["pgid"] = strconv.Itoa(pgid)
	}
	if opts.Lease > 0 {
		res.Expires = time.Now().UTC().Add(opts.Lease)
	}
	_ = s.reg.Register(res)
}

// Stop terminates an owned server and releases its resource record.
func (s *Server) Stop() {
	if !s.owned || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	killProcessGroup(s.cmd)
	_, _ = s.cmd.Process.Wait()
	if s.reg != nil && s.id != "" {
		_ = s.reg.Remove(s.id)
	}
}

// Reachable reports whether url answers any HTTP request.
func Reachable(ctx context.Context, url string, timeout time.Duration) bool {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// tailFile returns the last n lines of a file.
func tailFile(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
