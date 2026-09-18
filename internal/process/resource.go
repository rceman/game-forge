// Package process manages Game Forge's owned external resources.
//
// Every external resource Game Forge starts (a browser namespace, a local
// server, a benchmark process) is registered durably under
// ~/.game-forge/state/resources/ so it can be reclaimed after the owning run
// ends — normally or abnormally.
//
// Only resources Game Forge itself registered are ever reclaimed. Nothing here
// kills processes by executable name.
package process

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Resource kinds.
const (
	KindBrowser = "browser"
	KindServer  = "server"
)

// Resource is one owned external resource.
type Resource struct {
	// ID uniquely identifies the resource record.
	ID string `json:"id"`
	// RunID groups resources owned by one Game Forge invocation.
	RunID string `json:"run_id"`
	// Project is the owning project id, or "" for machine-scoped resources.
	Project string `json:"project,omitempty"`
	// Kind is one of the Kind* constants.
	Kind string `json:"kind"`
	// Provider names the owning provider, e.g. "agent-browser".
	Provider string `json:"provider"`
	// Namespace is the provider-side identifier used for targeted cleanup.
	Namespace string `json:"namespace,omitempty"`
	// PID is the provider process id where applicable.
	PID int `json:"pid,omitempty"`
	// Host is where the resource runs, e.g. "windows".
	Host string `json:"host,omitempty"`
	// Created is when the resource was registered.
	Created time.Time `json:"created"`
	// Expires is the lease deadline. Zero means no lease.
	Expires time.Time `json:"expires,omitempty"`
	// Metadata carries provider-specific identification details.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Expired reports whether the resource's lease has lapsed at now.
func (r *Resource) Expired(now time.Time) bool {
	return !r.Expires.IsZero() && !now.Before(r.Expires)
}

// Registry is a durable collection of owned resources.
type Registry struct {
	dir string
}

// Open returns the registry rooted at dir, creating it if needed.
func Open(dir string) (*Registry, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create resource directory: %w", err)
	}
	return &Registry{dir: dir}, nil
}

// Dir returns the registry directory.
func (r *Registry) Dir() string { return r.dir }

// path returns the on-disk path for a resource id.
func (r *Registry) path(id string) string {
	return filepath.Join(r.dir, id+".json")
}

// Register writes a resource record, assigning defaults for missing fields.
func (r *Registry) Register(res *Resource) error {
	if res.ID == "" {
		return fmt.Errorf("resource id is required")
	}
	if res.Kind == "" {
		return fmt.Errorf("resource kind is required")
	}
	if res.Provider == "" {
		return fmt.Errorf("resource provider is required")
	}
	if res.Created.IsZero() {
		res.Created = time.Now().UTC()
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return fmt.Errorf("encode resource: %w", err)
	}
	tmp := r.path(res.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write resource: %w", err)
	}
	if err := os.Rename(tmp, r.path(res.ID)); err != nil {
		return fmt.Errorf("commit resource: %w", err)
	}
	return nil
}

// List returns all registered resources, sorted by creation time.
func (r *Registry) List() ([]*Resource, error) {
	out := []*Resource{}
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, fmt.Errorf("read resource directory: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		res, err := r.Get(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, res)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out, nil
}

// Get reads one resource record.
func (r *Registry) Get(id string) (*Resource, error) {
	data, err := os.ReadFile(r.path(id))
	if err != nil {
		return nil, fmt.Errorf("read resource %s: %w", id, err)
	}
	res := &Resource{}
	if err := json.Unmarshal(data, res); err != nil {
		return nil, fmt.Errorf("parse resource %s: %w", id, err)
	}
	return res, nil
}

// Remove deletes a resource record.
func (r *Registry) Remove(id string) error {
	if err := os.Remove(r.path(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove resource %s: %w", id, err)
	}
	return nil
}

// Expired returns resources whose leases have lapsed at now.
func (r *Registry) Expired(now time.Time) ([]*Resource, error) {
	all, err := r.List()
	if err != nil {
		return nil, err
	}
	var out []*Resource
	for _, res := range all {
		if res.Expired(now) {
			out = append(out, res)
		}
	}
	return out, nil
}
