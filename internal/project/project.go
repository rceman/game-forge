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
	"time"

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
	// Server declares servers Game Forge starts, owns and stops.
	Server Servers `yaml:"server"`
	// Visual declares defaults for screenshot/sweep workflows.
	Visual Visual `yaml:"visual"`
	// GPU declares the benchmark scenario/state Game Forge drives.
	GPU GPU `yaml:"gpu"`
	// Profiles declares named validation profiles.
	Profiles map[string][]Stage `yaml:"profiles"`

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

// Duration is a YAML duration such as "90s".
type Duration time.Duration

// UnmarshalYAML parses a duration string.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// Std returns the standard-library duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Server describes one server Game Forge can own.
type Server struct {
	// Build is an optional command run before the server starts.
	Build []string `yaml:"build"`
	// Command starts the server.
	Command []string `yaml:"command"`
	// URL is polled for readiness and used to navigate the browser.
	URL string `yaml:"url"`
	// ReadyTimeout bounds the readiness wait.
	ReadyTimeout Duration `yaml:"ready_timeout"`
}

// Servers groups the declared servers.
type Servers struct {
	Dev  Server `yaml:"dev"`
	Prod Server `yaml:"prod"`
}

// Visual declares screenshot/sweep defaults.
type Visual struct {
	DefaultTicks int    `yaml:"default_ticks"`
	DefaultCase  string `yaml:"default_case"`
}

// GPU declares the benchmark scenario and state.
type GPU struct {
	Scenario string `yaml:"scenario"`
	Ticks    int    `yaml:"ticks"`
	Frames   int    `yaml:"frames"`
}

// Stage is one step of a validation profile. Exactly one mode is used:
// a profile reference (`uses`), a native command, a browser stage, a visual
// sweep, or a production smoke.
type Stage struct {
	Name string `yaml:"name"`
	// Uses runs another declared profile.
	Uses string `yaml:"uses"`
	// Command runs a native process; its exit code is the stage result.
	Command []string `yaml:"command"`
	// Browser runs the stage in a browser against a declared server.
	Browser bool `yaml:"browser"`
	// Eval is JavaScript evaluated in the page for a browser/prod stage.
	Eval string `yaml:"eval"`
	// Click is an optional selector Game Forge clicks (a trusted browser
	// input event) before evaluating. Needed for gesture-gated APIs such as
	// WebAudio.
	Click string `yaml:"click"`
	// Sweep runs the generic visual sweep over the project's visual cases.
	Sweep bool `yaml:"sweep"`
	// Prod runs the production build + smoke stage.
	Prod bool `yaml:"prod"`
	// Ticks overrides the advancement count for sweep/visual stages.
	Ticks int `yaml:"ticks"`
	// Console treats console errors as failures too.
	Console bool `yaml:"console"`
	// Timeout bounds the stage.
	Timeout Duration `yaml:"timeout"`
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
	for name, stages := range m.Profiles {
		if len(stages) == 0 {
			return fmt.Errorf("profile %q: at least one stage is required", name)
		}
		for i, s := range stages {
			if err := m.validateStage(name, i, s); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manifest) validateStage(profile string, i int, s Stage) error {
	label := fmt.Sprintf("profile %q stage %d", profile, i)
	if s.Name == "" {
		return fmt.Errorf("%s: name is required", label)
	}
	modes := 0
	if s.Uses != "" {
		modes++
		if _, ok := m.Profiles[s.Uses]; !ok {
			return fmt.Errorf("%s (%s): uses unknown profile %q", label, s.Name, s.Uses)
		}
	}
	if len(s.Command) > 0 {
		modes++
	}
	if s.Browser {
		modes++
		if s.Eval == "" && !s.Sweep {
			return fmt.Errorf("%s (%s): browser stage needs eval or sweep", label, s.Name)
		}
	}
	if s.Prod {
		modes++
		if s.Eval == "" {
			return fmt.Errorf("%s (%s): prod stage needs eval", label, s.Name)
		}
	}
	if s.Sweep {
		modes++
	}
	if modes != 1 {
		return fmt.Errorf("%s (%s): exactly one stage mode is required", label, s.Name)
	}
	return nil
}

// Adapter returns a declared adapter by capability name.
func (m *Manifest) Adapter(capability string) (Adapter, bool) {
	a, ok := m.Adapters[capability]
	return a, ok
}

// Profile returns a declared profile by name.
func (m *Manifest) Profile(name string) ([]Stage, bool) {
	s, ok := m.Profiles[name]
	return s, ok
}

// Supports reports whether the project declares a capability as enabled.
func (m *Manifest) Supports(capability string) bool {
	return m.Capabilities[capability]
}
