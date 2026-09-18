// Package daemon implements the Game Forge control daemon.
//
// The daemon is a boring transport: authenticate, decode, validate, dispatch to
// the Operation Registry, encode or stream. It owns the lifecycle of reusable
// Game Forge resources and runs periodic housekeeping in-process.
//
// Transport is one cross-platform mechanism: HTTP over loopback TCP on a
// dynamic OS-assigned port. There are no Unix-socket / named-pipe variants.
package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rceman/game-forge/internal/config"
	"github.com/rceman/game-forge/schemas"
)

// Protocol is the daemon protocol identifier.
const Protocol = schemas.Protocol

// Discovery is the ephemeral daemon discovery state. It lives in the run
// directory and is the only place the bearer token exists.
type Discovery struct {
	Protocol string `json:"protocol"`
	Endpoint string `json:"endpoint"`
	PID      int    `json:"pid"`
	Token    string `json:"token"`
}

// RunDir returns the ephemeral runtime directory (~/.game-forge/run).
func RunDir() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "run"), nil
}

// DiscoveryPath returns the discovery file path.
func DiscoveryPath() (string, error) {
	dir, err := RunDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.json"), nil
}

// LockPath returns the startup-coordination lock path.
func LockPath() (string, error) {
	dir, err := RunDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.lock"), nil
}

// LogPath returns the daemon log path.
func LogPath() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs", "daemon.log"), nil
}

// Read reads the discovery state, returning nil when absent or malformed.
// A malformed file is treated as stale, never as a hard error.
func Read() (*Discovery, error) {
	path, err := DiscoveryPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var d Discovery
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, nil
	}
	if d.Protocol != Protocol || d.Endpoint == "" || d.Token == "" || d.PID <= 0 {
		return nil, nil
	}
	return &d, nil
}

// Write atomically writes the discovery state with owner-only permissions.
// The file is written to a temporary sibling and renamed so a concurrent
// reader never sees a partial document.
func Write(d *Discovery) error {
	dir, err := RunDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "daemon-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	// Owner-only permissions before the rename, so the visible file is never
	// world-readable.
	_ = tmp.Chmod(0o600)
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	path, err := DiscoveryPath()
	if err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// Remove deletes the discovery state.
func Remove() error {
	path, err := DiscoveryPath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// newToken returns a 256-bit random bearer token.
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate daemon token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
