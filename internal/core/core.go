// Package core is Game Forge's reusable operation layer.
//
// Every externally callable capability lives here as an op.Handler that the
// Operation Registry dispatches. Frontends (the CLI over HTTP, the daemon, a
// future MCP adapter) supply an op.Request; this package owns the semantics.
//
// A Core is built per request: it resolves the machine config, the caller's
// project manifest (discovered from the request's cwd) and the durable resource
// registry. In daemon mode the Runtime also owns reusable long-lived resources
// (the shared dev server and browser session) across requests.
package core

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rceman/game-forge/internal/adapter"
	"github.com/rceman/game-forge/internal/browser"
	"github.com/rceman/game-forge/internal/config"
	"github.com/rceman/game-forge/internal/process"
	"github.com/rceman/game-forge/internal/project"
	"github.com/rceman/game-forge/internal/server"
)

// DefaultIdleTTL is how long a daemon-owned reusable resource stays alive while
// unused. Housekeeping reclaims it once it lapses.
const DefaultIdleTTL = 10 * time.Minute

// Viewport is the deterministic browser viewport used by every browser stage.
var Viewport = [2]int{1600, 900}

// Runtime owns shared, cross-request state: browser session locks and the set
// of in-flight runs that housekeeping must not reclaim. A daemon holds one
// Runtime for its whole life; a direct in-process run can use a fresh one.
type Runtime struct {
	// KeepAlive marks daemon-owned resources: they are leased and reused across
	// requests rather than stopped when a request finishes.
	KeepAlive bool
	// IdleTTL is the lease applied to daemon-owned reusable resources.
	IdleTTL time.Duration

	mu        sync.Mutex
	browserMu map[string]*sync.Mutex
	runs      map[string]bool
}

// NewRuntime returns a Runtime. keepAlive should be true under the daemon.
func NewRuntime(keepAlive bool) *Runtime {
	return &Runtime{
		KeepAlive: keepAlive,
		IdleTTL:   DefaultIdleTTL,
		browserMu: map[string]*sync.Mutex{},
		runs:      map[string]bool{},
	}
}

// beginRun marks a run id as in-flight until the returned function is called.
func (rt *Runtime) beginRun(id string) func() {
	rt.mu.Lock()
	rt.runs[id] = true
	rt.mu.Unlock()
	return func() {
		rt.mu.Lock()
		delete(rt.runs, id)
		rt.mu.Unlock()
	}
}

// ActiveRuns returns the in-flight run ids.
func (rt *Runtime) ActiveRuns() map[string]bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := make(map[string]bool, len(rt.runs))
	for k := range rt.runs {
		out[k] = true
	}
	return out
}

// lockBrowser serializes access to one browser namespace. A shared browser
// session cannot serve two navigations at once, so concurrent requests queue.
func (rt *Runtime) lockBrowser(ns string) func() {
	rt.mu.Lock()
	m, ok := rt.browserMu[ns]
	if !ok {
		m = &sync.Mutex{}
		rt.browserMu[ns] = m
	}
	rt.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// Core executes operations for one request.
type Core struct {
	rt      *Runtime
	cwd     string
	cfg     *config.Config
	reg     *process.Registry
	runID   string
	logDir  string
	lease   time.Duration
	unmuted bool

	mOnce sync.Once
	m     *project.Manifest
	mErr  error

	release    func()
	newBrowser func(cfg *config.Config, ns string) browser.Provider
}

var runCounter atomic.Int64

// New builds a Core for one request whose cwd is the caller's.
func New(rt *Runtime, cwd string) (*Core, error) {
	return newCore(rt, cwd, "")
}

// newCore builds a Core, using runID as its resource-ownership group when
// given (the daemon passes its stream run id so `ps` correlates resources to
// the run that created them).
func newCore(rt *Runtime, cwd, runID string) (*Core, error) {
	if rt == nil {
		rt = NewRuntime(false)
	}
	cfg, _, err := config.LoadDefault()
	if err != nil {
		return nil, err
	}
	reg, err := openRegistry()
	if err != nil {
		return nil, err
	}
	stateDir, err := config.StateDir()
	if err != nil {
		return nil, err
	}
	logDir := filepath.Join(stateDir, "logs")
	_ = os.MkdirAll(logDir, 0o755)
	lease := 10 * time.Minute
	if rt.KeepAlive {
		lease = rt.IdleTTL
	}
	if runID == "" {
		runID = fmt.Sprintf("%d-%d-%d", time.Now().UTC().Unix(), os.Getpid(), runCounter.Add(1))
	}
	c := &Core{
		rt:         rt,
		cwd:        cwd,
		cfg:        cfg,
		reg:        reg,
		runID:      runID,
		logDir:     logDir,
		lease:      lease,
		newBrowser: func(cfg *config.Config, ns string) browser.Provider { return browser.NewAgentBrowser(cfg, ns) },
	}
	// The run is in-flight for the Core's lifetime so housekeeping never
	// reclaims resources it is actively using.
	c.release = rt.beginRun(c.runID)
	return c, nil
}

// Close releases the in-flight run marker. Daemon-owned resources are leased,
// not stopped, so Close does not tear them down.
func (c *Core) Close() {
	if c.release != nil {
		c.release()
		c.release = nil
	}
}

// manifest discovers and caches the project manifest for the request cwd.
func (c *Core) manifest() (*project.Manifest, error) {
	c.mOnce.Do(func() {
		cwd := c.cwd
		if cwd == "" {
			var err error
			cwd, err = os.Getwd()
			if err != nil {
				c.mErr = err
				return
			}
		}
		c.m, c.mErr = project.DiscoverAndLoad(cwd)
	})
	return c.m, c.mErr
}

// Config returns the machine config.
func (c *Core) Config() *config.Config { return c.cfg }

// Registry returns the durable resource registry.
func (c *Core) Registry() *process.Registry { return c.reg }

// RunID returns this request's run identifier.
func (c *Core) RunID() string { return c.runID }

// openRegistry opens the durable resource registry.
func openRegistry() (*process.Registry, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	return process.Open(filepath.Join(dir, "state", "resources"))
}

// namespaceFor builds an agent-browser namespace for a one-off session.
func (c *Core) namespaceFor(suffix string) string {
	prefix := c.cfg.Browser.AgentBrowser.NamespacePrefix
	if prefix == "" {
		prefix = "game-forge"
	}
	return prefix + "-" + suffix
}

// projectKey returns the stable per-checkout project identity: the human
// project id plus a short hash of the canonical project root, e.g.
// "spin-tower-a81c92". Two checkouts may share a project.id but never share a
// key, so daemon-owned resources stay project-isolated.
func (c *Core) projectKey() (string, error) {
	m, err := c.manifest()
	if err != nil {
		return "", err
	}
	return projectKeyFor(m), nil
}

// projectKeyFor derives the project key from a manifest.
func projectKeyFor(m *project.Manifest) string {
	root := canonicalRoot(m.Root)
	sum := sha256.Sum256([]byte(root))
	return fmt.Sprintf("%s-%x", sanitize(m.Project.ID), sum[:3])
}

// canonicalRoot resolves symlinks and absolutizes a project root so the same
// checkout always hashes identically.
func canonicalRoot(root string) string {
	if p, err := filepath.EvalSymlinks(root); err == nil {
		root = p
	}
	if p, err := filepath.Abs(root); err == nil {
		root = p
	}
	return filepath.Clean(root)
}

// ownsResource reports whether a registry record belongs to this project.
// Keyed records match on ProjectKey; a legacy record without a key falls back
// to the human project id.
func ownsResource(r *process.Resource, key, id string) bool {
	if r.ProjectKey != "" {
		return r.ProjectKey == key
	}
	return r.Project == id
}

// sharedNamespace returns the stable namespace for the reused daemon-owned
// browser session of a project. It is keyed by project root so two checkouts
// with the same project.id never attach to one agent-browser namespace.
// Unmuted sessions get their own namespace so a diagnostic override never
// relaunches a normal run's browser.
func (c *Core) sharedNamespace() (string, error) {
	m, err := c.manifest()
	if err != nil {
		return "", err
	}
	id := projectKeyFor(m)
	if c.unmuted {
		id += "-unmuted"
	}
	return c.namespaceFor(id), nil
}

// sanitize reduces a project id to characters safe for a provider namespace.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "project"
	}
	return out
}

// adapter returns a running simulation adapter and its cleanup.
func (c *Core) adapter(ctx context.Context) (*adapter.Client, func(), error) {
	m, err := c.manifest()
	if err != nil {
		return nil, nil, err
	}
	a, ok := m.Adapter("simulation")
	if !ok {
		return nil, nil, fmt.Errorf("project %q declares no simulation adapter", m.Project.ID)
	}
	client, err := adapter.Start(m.Root, a.Command, os.Stderr)
	if err != nil {
		return nil, nil, err
	}
	return client, func() { _ = client.Close() }, nil
}

// serverSpec returns the declared server of kind, or an error.
func (c *Core) serverSpec(kind string) (project.Server, error) {
	m, err := c.manifest()
	if err != nil {
		return project.Server{}, err
	}
	var sc project.Server
	switch kind {
	case "dev":
		sc = m.Server.Dev
	case "prod":
		sc = m.Server.Prod
	default:
		return project.Server{}, fmt.Errorf("unknown server %q", kind)
	}
	if sc.URL == "" {
		return project.Server{}, fmt.Errorf("project declares no %q server", kind)
	}
	return sc, nil
}

// ensureServer starts or reuses a declared server.
//
// In daemon mode the server is leased and reused across requests. A reused
// server keeps running; its lease is refreshed so housekeeping does not
// reclaim it while it is actively serving.
func (c *Core) ensureServer(ctx context.Context, kind string, lease time.Duration) (*server.Server, error) {
	m, err := c.manifest()
	if err != nil {
		return nil, err
	}
	sc, err := c.serverSpec(kind)
	if err != nil {
		return nil, err
	}
	key := projectKeyFor(m)
	if lease <= 0 {
		lease = c.lease
	}
	srv, err := server.Ensure(ctx, server.Options{
		Name:         kind,
		Command:      sc.Command,
		URL:          sc.URL,
		ReadyTimeout: sc.ReadyTimeout.Std(),
		Dir:          m.Root,
		LogDir:       c.logDir,
		RunID:        c.runID,
		Project:      m.Project.ID,
		ProjectKey:   key,
		Lease:        lease,
		Registry:     c.reg,
	})
	if err != nil {
		return nil, err
	}
	c.refreshServerLease(kind, key, lease)
	return srv, nil
}

// refreshServerLease extends the lease of this project's registered record
// for kind. Another project's same-named server is never touched.
func (c *Core) refreshServerLease(kind, key string, lease time.Duration) {
	if lease <= 0 {
		return
	}
	m, err := c.manifest()
	if err != nil {
		return
	}
	all, err := c.reg.List()
	if err != nil {
		return
	}
	for _, r := range all {
		if r.Kind != process.KindServer || r.Metadata["name"] != kind {
			continue
		}
		if !ownsResource(r, key, m.Project.ID) {
			continue
		}
		r.Expires = time.Now().UTC().Add(lease)
		_ = c.reg.Register(r)
	}
}

// stopNamedServer stops this project's named server if it is registered and
// owned. A same-named server belonging to another project is never stopped.
func (c *Core) stopNamedServer(kind string) (id string, pid int, stopped bool, err error) {
	m, err := c.manifest()
	if err != nil {
		return "", 0, false, err
	}
	if _, err := c.serverSpec(kind); err != nil {
		return "", 0, false, err
	}
	key := projectKeyFor(m)
	all, err := c.reg.List()
	if err != nil {
		return "", 0, false, err
	}
	for _, r := range all {
		if r.Kind != process.KindServer || r.Metadata["name"] != kind {
			continue
		}
		if !ownsResource(r, key, m.Project.ID) {
			continue
		}
		pgid, _ := strconv.Atoi(r.Metadata["pgid"])
		server.KillProcessGroup(r.PID, pgid)
		if err := c.reg.Remove(r.ID); err != nil {
			return r.ID, r.PID, true, err
		}
		return r.ID, r.PID, true, nil
	}
	return "", 0, false, nil
}

// openBrowser opens a session, registering it as an owned resource. In daemon
// mode the session uses the shared, reused namespace and is left running; in a
// direct run it uses a per-request namespace and the returned cleanup releases
// it.
func (c *Core) openBrowser(ctx context.Context, url string) (*browser.Session, func(), error) {
	return c.openBrowserMode(ctx, url, true)
}

// openBrowserRaw opens a session without waiting for the project bridge, for
// production builds that do not expose it.
func (c *Core) openBrowserRaw(ctx context.Context, url string) (*browser.Session, func(), error) {
	return c.openBrowserMode(ctx, url, false)
}

func (c *Core) openBrowserMode(ctx context.Context, url string, waitBridge bool) (*browser.Session, func(), error) {
	m, err := c.manifest()
	if err != nil {
		return nil, nil, err
	}
	ns := c.namespaceFor(c.runID)
	shared := false
	if c.rt.KeepAlive {
		var err error
		ns, err = c.sharedNamespace()
		if err != nil {
			return nil, nil, err
		}
		shared = true
	}
	// The namespace lock is held for the WHOLE logical browser operation, not
	// just the open: one shared session cannot serve two concurrent
	// navigations. The returned cleanup releases it; every error path unlocks
	// exactly once.
	unlock := c.rt.lockBrowser(ns)

	provider := c.newBrowser(c.cfg, ns)
	if c.unmuted {
		if sp, ok := provider.(interface{ SetUnmuted(bool) }); ok {
			sp.SetUnmuted(true)
		}
	}
	res := &process.Resource{
		ID:         "browser-" + ns,
		RunID:      c.runID,
		Project:    m.Project.ID,
		ProjectKey: projectKeyFor(m),
		Kind:       process.KindBrowser,
		Provider:   c.cfg.Browser.Provider,
		Namespace:  ns,
		Host:       c.cfg.Browser.Host,
		Expires:    time.Now().UTC().Add(c.lease),
	}
	var (
		sess *browser.Session
		serr error
	)
	if waitBridge {
		sess, serr = browser.OpenSession(ctx, provider, url, Viewport)
	} else {
		sess, serr = browser.OpenSessionRaw(ctx, provider, url, Viewport)
	}
	if serr != nil {
		unlock()
		return nil, nil, fmt.Errorf("open browser at %s: %w", url, serr)
	}
	if err := c.reg.Register(res); err != nil {
		_ = sess.Close(context.Background())
		unlock()
		return nil, nil, fmt.Errorf("register browser: %w", err)
	}
	if shared {
		// A daemon-owned session is leased for reuse: cleanup releases the
		// operation lock but must NOT close the reusable browser.
		return sess, unlock, nil
	}
	cleanup := func() {
		_ = sess.Close(context.Background())
		_ = c.reg.Remove(res.ID)
		unlock()
	}
	return sess, cleanup, nil
}

// reclaimResource releases one owned resource through its provider.
func (c *Core) reclaimResource(ctx context.Context, r *process.Resource) error {
	switch r.Kind {
	case process.KindBrowser:
		p := browser.NewAgentBrowser(c.cfg, r.Namespace)
		return p.Close(ctx)
	case process.KindServer:
		pgid, _ := strconv.Atoi(r.Metadata["pgid"])
		// Only a process Game Forge registered is terminated, by pid/pgid.
		server.KillProcessGroup(r.PID, pgid)
		return nil
	default:
		return fmt.Errorf("no reclaimer for kind %q", r.Kind)
	}
}

// tick reclaims every expired owned resource, skipping resources that belong to
// an in-flight run. It is idempotent: a second pass finds nothing to do.
func (c *Core) tick(ctx context.Context) (*ReclaimResult, error) {
	res := &ReclaimResult{Reclaimed: []Reclaimed{}}
	expired, err := c.reg.Expired(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	active := c.rt.ActiveRuns()
	for _, r := range expired {
		if active[r.RunID] {
			continue
		}
		if err := c.reclaimResource(ctx, r); err != nil {
			res.Failed++
			continue
		}
		if err := c.reg.Remove(r.ID); err != nil {
			res.Failed++
			continue
		}
		res.Reclaimed = append(res.Reclaimed, Reclaimed{
			ID: r.ID, Kind: r.Kind, Provider: r.Provider, Namespace: r.Namespace,
		})
	}
	return res, nil
}

// Reclaimed describes one resource that was released.
type Reclaimed struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Provider  string `json:"provider"`
	Namespace string `json:"namespace,omitempty"`
}

// ReclaimResult is the result of a housekeeping pass.
type ReclaimResult struct {
	Reclaimed []Reclaimed `json:"reclaimed"`
	Failed    int         `json:"failed"`
}

// Tick performs one housekeeping pass for the resource.tick operation.
func (c *Core) Tick(ctx context.Context) (*ReclaimResult, error) {
	return c.tick(ctx)
}

// ReleaseOwned reclaims every owned resource regardless of lease or active run.
// The daemon calls it on graceful shutdown so a stopped daemon leaves no
// Game Forge-owned resources behind.
func (c *Core) ReleaseOwned(ctx context.Context) (*ReclaimResult, error) {
	res := &ReclaimResult{Reclaimed: []Reclaimed{}}
	all, err := c.reg.List()
	if err != nil {
		return nil, err
	}
	for _, r := range all {
		if err := c.reclaimResource(ctx, r); err != nil {
			res.Failed++
			continue
		}
		if err := c.reg.Remove(r.ID); err != nil {
			res.Failed++
			continue
		}
		res.Reclaimed = append(res.Reclaimed, Reclaimed{
			ID: r.ID, Kind: r.Kind, Provider: r.Provider, Namespace: r.Namespace,
		})
	}
	return res, nil
}
