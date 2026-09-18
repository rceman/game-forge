// Package config loads machine-level Game Forge configuration.
//
// Machine/tool/provider configuration lives outside game repositories, at
// ~/.game-forge/config.yaml. Project-level configuration lives in the game
// repository (game-forge.yaml) and is handled by package project.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// CurrentVersion is the config schema version understood by this build.
const CurrentVersion = 1

// Config is the machine-level Game Forge configuration.
type Config struct {
	Version int     `yaml:"version"`
	Browser Browser `yaml:"browser"`
}

// Browser selects and configures the browser provider.
type Browser struct {
	// Provider names the browser automation provider, e.g. "agent-browser".
	Provider string `yaml:"provider"`
	// Host is where the provider runs: "windows", "linux", or "" (native).
	Host string `yaml:"host"`
	// AgentBrowser holds provider-specific settings. Provider-specific
	// executable paths must live here, never in a project manifest.
	AgentBrowser AgentBrowser `yaml:"agent_browser"`
}

// AgentBrowser configures the agent-browser provider.
type AgentBrowser struct {
	// CLI is the absolute path to the agent-browser launcher on the provider
	// host (e.g. a Windows .cmd). Empty means resolve from PATH.
	CLI string `yaml:"cli"`
	// Chrome is the absolute path to the browser executable. Empty means the
	// provider default.
	Chrome string `yaml:"chrome"`
	// Headless selects headless mode. Defaults to true.
	Headless bool `yaml:"headless"`
	// Unmuted disables Game Forge's default browser audio-output suppression.
	// Automated runs are silent by default; this is a diagnostic opt-in.
	Unmuted bool `yaml:"unmuted"`
	// NamespacePrefix prefixes generated agent-browser namespaces so Game
	// Forge can identify and reclaim the resources it owns.
	NamespacePrefix string `yaml:"namespace_prefix"`
}

// Dir returns the Game Forge machine state directory (~/.game-forge).
func Dir() (string, error) {
	if v := os.Getenv("GAME_FORGE_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".game-forge"), nil
}

// Path returns the machine config file path (~/.game-forge/config.yaml).
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// StateDir returns the durable state directory (~/.game-forge/state).
func StateDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state"), nil
}

// Default returns the built-in configuration used when no config file exists.
func Default() *Config {
	return &Config{
		Version: CurrentVersion,
		Browser: Browser{
			Provider: "agent-browser",
			AgentBrowser: AgentBrowser{
				Headless:        true,
				NamespacePrefix: "game-forge",
			},
		},
	}
}

// Load reads the machine config from path, returning Default when the file
// does not exist.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Default(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.Version == 0 {
		cfg.Version = CurrentVersion
	}
	if cfg.Version != CurrentVersion {
		return nil, fmt.Errorf("config %s: unsupported version %d (want %d)", path, cfg.Version, CurrentVersion)
	}
	return cfg, nil
}

// LoadDefault loads the machine config from the standard location.
func LoadDefault() (*Config, string, error) {
	path, err := Path()
	if err != nil {
		return nil, "", err
	}
	cfg, err := Load(path)
	return cfg, path, err
}

// Save writes the configuration to path, creating parent directories.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}
