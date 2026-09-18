// Package contract defines the versioned Game Forge project contract.
//
// The contract identifier ("game-forge/v1") is the stable, language-agnostic
// handshake between a game project and Game Forge. It is intentionally tiny:
// it describes what a project can do, never how Game Forge does its job.
package contract

import (
	"fmt"
	"strings"
)

// Current is the contract version implemented by this build.
const Current = "game-forge/v1"

// Parse validates a contract identifier and returns its major version.
func Parse(id string) (int, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return 0, fmt.Errorf("empty contract identifier")
	}
	name, version, ok := strings.Cut(id, "/")
	if !ok || name == "" || version == "" {
		return 0, fmt.Errorf("malformed contract %q (want \"<name>/v<major>\")", id)
	}
	if name != "game-forge" {
		return 0, fmt.Errorf("unknown contract %q (want %q)", id, Current)
	}
	if !strings.HasPrefix(version, "v") {
		return 0, fmt.Errorf("malformed contract version %q (want \"v<major>\")", version)
	}
	var major int
	if _, err := fmt.Sscanf(version[1:], "%d", &major); err != nil {
		return 0, fmt.Errorf("malformed contract version %q: %w", version, err)
	}
	if major != 1 {
		return 0, fmt.Errorf("unsupported contract %q (this build implements %q)", id, Current)
	}
	return major, nil
}

// Supported reports whether id is a contract this build understands.
func Supported(id string) bool {
	_, err := Parse(id)
	return err == nil
}
