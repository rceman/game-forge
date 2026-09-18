// Package project discovers and reads project-level Game Forge configuration.
//
// A project is any directory tree containing a game-forge.yaml manifest. Game
// Forge locates a project by walking upward from the current directory. The
// manifest declares the project contract, capabilities and adapters; it must
// not contain machine-specific tool paths.
package project

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/rceman/game-forge/internal/contract"
)

// ManifestName is the project manifest filename.
const ManifestName = "game-forge.yaml"

// Manifest is the project-level configuration.
type Manifest struct {
	// Contract is the project contract identifier, e.g. "game-forge/v1".
	Contract string `yaml:"contract"`
	// Project identifies the project.
	Project Project `yaml:"project"`
	// Capabilities declares which generic capabilities the project supports.
	Capabilities map[string]bool `yaml:"capabilities"`
	// Adapters maps generic capability names to their declarations.
	Adapters map[string]Adapter `yaml:"adapters"`

	// Root is the directory containing the manifest. Not part of the file.
	Root string `yaml:"-"`
}

// Project identifies a game project.
type Project struct {
	ID   string `yaml:"id"`
	Type string `yaml:"type"`
}

// Adapter declares how Game Forge invokes one generic capability. Adapters are
// transport declarations only; they carry no game semantics.
type Adapter struct {
	// Command is the argv used to launch a stdio adapter.
	Command []string `yaml:"command"`
	// Protocol names the wire protocol, e.g. "jsonl".
	Protocol string `yaml:"protocol"`
}

// Discover walks upward from start until it finds a manifest, returning the
// directory that contains it.
func Discover(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve start directory: %w", err)
	}
	for {
		candidate := filepath.Join(dir, ManifestName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s found in %s or any parent directory", ManifestName, start)
		}
		dir = parent
	}
}

// Load reads and validates the manifest in root.
func Load(root string) (*Manifest, error) {
	path := filepath.Join(root, ManifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	m := &Manifest{}
	if err := yaml.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	m.Root = root
	return m, nil
}

// DiscoverAndLoad finds the nearest project and reads its manifest.
func DiscoverAndLoad(start string) (*Manifest, error) {
	root, err := Discover(start)
	if err != nil {
		return nil, err
	}
	return Load(root)
}

// Validate checks the manifest for contract and identity errors.
func (m *Manifest) Validate() error {
	if _, err := contract.Parse(m.Contract); err != nil {
		return err
	}
	if m.Project.ID == "" {
		return fmt.Errorf("project.id is required")
	}
	for name, a := range m.Adapters {
		if len(a.Command) == 0 {
			return fmt.Errorf("adapter %q: command is required", name)
		}
		if a.Protocol == "" {
			return fmt.Errorf("adapter %q: protocol is required", name)
		}
	}
	return nil
}

// Supports reports whether the project declares a capability as enabled.
func (m *Manifest) Supports(capability string) bool {
	return m.Capabilities[capability]
}
