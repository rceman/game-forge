package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/rceman/game-forge/internal/daemon"
)

const (
	// startupTimeout bounds the wait for a daemon to become healthy.
	startupTimeout = 30 * time.Second
	// lockStaleAfter is how old a startup lock may be before it is treated as
	// abandoned by a crashed starter.
	lockStaleAfter = 30 * time.Second
)

// start coordinates daemon startup. Concurrent callers converge on one
// daemon through a lock file; a stale lock is recovered rather than waited on
// forever.
func start(ctx context.Context) (*Client, error) {
	lock, err := daemon.LockPath()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(startupTimeout)

	for {
		// Another starter may already have produced a healthy daemon.
		if c := Connect(ctx); c != nil {
			return c, nil
		}
		f, err := acquireLock(lock)
		if err == nil {
			// We hold the lock: spawn the daemon and wait for it.
			c, spawnErr := spawnAndWait(ctx)
			_ = f.Close()
			_ = os.Remove(lock)
			if spawnErr != nil {
				return nil, spawnErr
			}
			return c, nil
		}
		if !errors.Is(err, errLockHeld) {
			return nil, err
		}
		// Someone else holds the lock. If it is stale, reclaim it; otherwise
		// wait for the daemon they are starting.
		if info, statErr := os.Stat(lock); statErr == nil && time.Since(info.ModTime()) > lockStaleAfter {
			_ = os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for game-forged to start")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

var errLockHeld = fmt.Errorf("daemon startup lock is held")

// acquireLock attempts to create the startup lock atomically.
func acquireLock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, errLockHeld
		}
		return nil, err
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return f, nil
}

// spawnAndWait launches a detached daemon and waits for it to report healthy.
func spawnAndWait(ctx context.Context) (*Client, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	logPath, err := daemon.LogPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()

	cmd := exec.Command(exe, "daemon", "serve")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	daemon.Detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start game-forged: %w", err)
	}
	// The daemon must outlive this process; release it so we never wait on it.
	_ = cmd.Process.Release()

	deadline := time.Now().Add(startupTimeout)
	for time.Now().Before(deadline) {
		if c := Connect(ctx); c != nil {
			return c, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("game-forged did not become healthy within %s", startupTimeout)
}

// isLoopback reports whether an endpoint string is a loopback URL. It is used
// in tests to prove the daemon never binds a public address.
func isLoopback(endpoint string) bool {
	var u struct{ Host string }
	_ = u
	host := ""
	if i := len("http://"); len(endpoint) > i {
		host = endpoint[i:]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}
