// The project registry is the durable machine-local map from stable
// human/agent-facing project codes (TDG, BDG) to canonical project roots.
//
// Three identities exist and are never conflated:
//
//	code       "TDG"               stable alias the user/agent picks
//	root       /home/u/git/td-game machine-local manifest directory
//	projectKey "spin-tower-37de03" internal resource identity
//
// The registry lives under durable state (survives daemon restarts), is
// written atomically, is re-read on every resolution so daemon processes see
// CRUD immediately, and stores no credentials, PIDs or transient state.
package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/rceman/game-forge/internal/config"
	"github.com/rceman/game-forge/internal/op"
)

// MaxCodeLen bounds project codes so MCP calls stay compact.
const MaxCodeLen = 16

// Registration is one durable registry entry.
type Registration struct {
	// Code is the stable user-chosen alias (canonical uppercase).
	Code string `json:"code"`
	// Root is the canonical directory containing game-forge.yaml.
	Root string `json:"root"`
	// ProjectID is the manifest's project.id, recorded at registration.
	ProjectID string `json:"projectId"`
	// ProjectKey is the internal resource identity derived at registration.
	ProjectKey string `json:"projectKey"`
}

// LookupError is a structured resolution failure surfaced to callers.
type LookupError struct {
	// Code is a stable wire code: unknown_project, project_unavailable or
	// project_changed.
	Code string
	Msg  string
}

func (e *LookupError) Error() string { return e.Code + ": " + e.Msg }

// registryFile is the on-disk registry document.
type registryFile struct {
	Version  int             `json:"version"`
	Projects []*Registration `json:"projects"`
}

// Registry is the durable project registry. Writers are serialized through a
// mutex; readers re-read the file so a running daemon sees CLI registration
// changes without restart.
type Registry struct {
	path string
	mu   sync.Mutex
}

// RegistryPath returns the durable registry file path.
func RegistryPath() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "projects.json"), nil
}

// OpenRegistry opens the durable registry. It does not require the daemon.
func OpenRegistry() (*Registry, error) {
	path, err := RegistryPath()
	if err != nil {
		return nil, err
	}
	return &Registry{path: path}, nil
}

// NormalizeCode canonicalizes a project code: uppercase, 1-16 chars of
// [A-Z0-9_-]. Whitespace, empty and path-like values are rejected.
func NormalizeCode(code string) (string, error) {
	c := strings.ToUpper(strings.TrimSpace(code))
	if c == "" {
		return "", fmt.Errorf("project code is required")
	}
	if len(c) > MaxCodeLen {
		return "", fmt.Errorf("project code %q exceeds %d characters", code, MaxCodeLen)
	}
	for _, r := range c {
		ok := (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			return "", fmt.Errorf("project code %q: only letters, digits, '-' and '_' are allowed", code)
		}
	}
	return c, nil
}

// load reads the registry file. A missing or malformed file yields an empty
// registry; corruption is reported rather than silently dropped.
func (r *Registry) load() (*registryFile, error) {
	data, err := os.ReadFile(r.path)
	if os.IsNotExist(err) {
		return &registryFile{Version: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	var f registryFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse project registry %s: %w", r.path, err)
	}
	return &f, nil
}

// store writes the registry atomically with owner-only permissions.
func (r *Registry) store(f *registryFile) error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.path), "projects-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err == nil {
		_ = tmp.Chmod(0o600)
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, r.path)
}

// Add registers code for the project discovered from folder (the nearest
// game-forge.yaml's directory). The manifest is validated and the canonical
// root and existing projectKey algorithm are used. Without replace, an
// existing code is never silently remapped.
func (r *Registry) Add(code, folder string, replace bool) (*Registration, error) {
	c, err := NormalizeCode(code)
	if err != nil {
		return nil, err
	}
	m, err := DiscoverAndLoad(folder)
	if err != nil {
		return nil, err
	}
	root := CanonicalRoot(m.Root)
	reg := &Registration{
		Code:       c,
		Root:       root,
		ProjectID:  m.Project.ID,
		ProjectKey: KeyFor(m),
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := r.load()
	if err != nil {
		return nil, err
	}
	for i, p := range f.Projects {
		if p.Code == c {
			if !replace {
				return nil, fmt.Errorf("project code %s is already registered to %s (use --replace)", c, p.Root)
			}
			f.Projects[i] = reg
			return reg, r.store(f)
		}
	}
	f.Projects = append(f.Projects, reg)
	return reg, r.store(f)
}

// Remove deletes a registration. Removing an unknown code is an error.
func (r *Registry) Remove(code string) error {
	c, err := NormalizeCode(code)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := r.load()
	if err != nil {
		return err
	}
	for i, p := range f.Projects {
		if p.Code == c {
			f.Projects = append(f.Projects[:i], f.Projects[i+1:]...)
			return r.store(f)
		}
	}
	return fmt.Errorf("project code %s is not registered", c)
}

// Get returns the registration for code, if present.
func (r *Registry) Get(code string) (*Registration, bool) {
	c, err := NormalizeCode(code)
	if err != nil {
		return nil, false
	}
	f, err := r.load()
	if err != nil {
		return nil, false
	}
	for _, p := range f.Projects {
		if p.Code == c {
			return p, true
		}
	}
	return nil, false
}

// List returns all registrations sorted by code.
func (r *Registry) List() []*Registration {
	f, err := r.load()
	if err != nil {
		return nil
	}
	out := append([]*Registration(nil), f.Projects...)
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// Resolve maps a project code to its canonical project root and verifies the
// registration is still accurate: the directory must exist and must contain
// the same project identity it was registered with.
func (r *Registry) Resolve(code string) (string, *LookupError) {
	c, err := NormalizeCode(code)
	if err != nil {
		return "", &LookupError{Code: op.CodeUnknownProject, Msg: err.Error()}
	}
	reg, ok := r.Get(c)
	if !ok {
		return "", &LookupError{Code: op.CodeUnknownProject, Msg: c}
	}
	info, err := os.Stat(reg.Root)
	if err != nil || !info.IsDir() {
		return "", &LookupError{
			Code: op.CodeProjectUnavailable,
			Msg:  fmt.Sprintf("registered project %s does not exist at %s", c, reg.Root),
		}
	}
	m, err := Load(reg.Root)
	if err != nil {
		return "", &LookupError{
			Code: op.CodeProjectUnavailable,
			Msg:  fmt.Sprintf("registered project %s is not loadable: %v", c, err),
		}
	}
	if m.Project.ID != reg.ProjectID || KeyFor(m) != reg.ProjectKey {
		return "", &LookupError{
			Code: op.CodeProjectChanged,
			Msg:  fmt.Sprintf("project %s no longer matches the registered identity (re-register with --replace)", c),
		}
	}
	return reg.Root, nil
}
