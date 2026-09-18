package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/rceman/game-forge/internal/adapter"
	"github.com/rceman/game-forge/internal/browser"
	"github.com/rceman/game-forge/internal/config"
	"github.com/rceman/game-forge/internal/process"
	"github.com/rceman/game-forge/internal/project"
	"github.com/rceman/game-forge/internal/server"
)

// Viewport is the deterministic browser viewport used by every browser stage.
var Viewport = [2]int{1600, 900}

// harness holds the collaborators shared by the project-facing commands.
type harness struct {
	cfg    *config.Config
	m      *project.Manifest
	reg    *process.Registry
	runID  string
	logDir string
	seq    int64
	// lease bounds how long a resource this run owns stays reclaimable after
	// an abnormal exit.
	lease time.Duration
	// unmuted opts out of Game Forge's default browser audio-output
	// suppression (a diagnostic override).
	unmuted bool

	ownedServers []*server.Server
}

// newHarness loads machine config, discovers the project and opens the registry.
func newHarness() (*harness, error) {
	cfg, _, err := config.LoadDefault()
	if err != nil {
		return nil, err
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	m, err := project.DiscoverAndLoad(wd)
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
	return &harness{cfg: cfg, m: m, reg: reg, runID: newRunID(), logDir: logDir, lease: 10 * time.Minute}, nil
}

// adapter returns a running simulation adapter and its cleanup.
func (h *harness) adapter(ctx context.Context) (*adapter.Client, func(), error) {
	a, ok := h.m.Adapter("simulation")
	if !ok {
		return nil, nil, fmt.Errorf("project %q declares no simulation adapter", h.m.Project.ID)
	}
	client, err := adapter.Start(h.m.Root, a.Command, os.Stderr)
	if err != nil {
		return nil, nil, err
	}
	return client, func() { _ = client.Close() }, nil
}

// openBrowser opens a browser session, registering it as an owned resource.
func (h *harness) openBrowser(ctx context.Context, url string) (*browser.Session, func(), error) {
	return h.openBrowserMode(ctx, url, true)
}

// openBrowserRaw opens a browser session without waiting for the project
// bridge, for production builds that do not expose it.
func (h *harness) openBrowserRaw(ctx context.Context, url string) (*browser.Session, func(), error) {
	return h.openBrowserMode(ctx, url, false)
}

func (h *harness) openBrowserMode(ctx context.Context, url string, waitBridge bool) (*browser.Session, func(), error) {
	ns := namespaceFor(h.cfg, fmt.Sprintf("%s-%d", h.runID, atomic.AddInt64(&h.seq, 1)))
	res := &process.Resource{
		ID:        "browser-" + ns,
		RunID:     h.runID,
		Project:   h.m.Project.ID,
		Kind:      process.KindBrowser,
		Provider:  h.cfg.Browser.Provider,
		Namespace: ns,
		Host:      h.cfg.Browser.Host,
		Expires:   time.Now().UTC().Add(h.lease),
	}
	if err := h.reg.Register(res); err != nil {
		return nil, nil, err
	}
	provider := browser.NewAgentBrowser(h.cfg, ns)
	if h.unmuted {
		provider.SetUnmuted(true)
	}
	var (
		sess *browser.Session
		err  error
	)
	if waitBridge {
		sess, err = browser.OpenSession(ctx, provider, url, Viewport)
	} else {
		sess, err = browser.OpenSessionRaw(ctx, provider, url, Viewport)
	}
	if err != nil {
		_ = provider.Close(context.Background())
		_ = h.reg.Remove(res.ID)
		return nil, nil, err
	}
	cleanup := func() {
		_ = provider.Close(context.Background())
		_ = h.reg.Remove(res.ID)
	}
	return sess, cleanup, nil
}

// ensureServer starts or reuses a declared server.
func (h *harness) ensureServer(ctx context.Context, kind string, lease time.Duration) (*server.Server, error) {
	if lease <= 0 {
		lease = h.lease
	}
	var sc project.Server
	switch kind {
	case "dev":
		sc = h.m.Server.Dev
	case "prod":
		sc = h.m.Server.Prod
	default:
		return nil, fmt.Errorf("unknown server %q", kind)
	}
	if sc.URL == "" {
		return nil, fmt.Errorf("project declares no %q server", kind)
	}
	srv, err := server.Ensure(ctx, server.Options{
		Name:         kind,
		Command:      sc.Command,
		URL:          sc.URL,
		ReadyTimeout: sc.ReadyTimeout.Std(),
		Dir:          h.m.Root,
		LogDir:       h.logDir,
		RunID:        h.runID,
		Project:      h.m.Project.ID,
		Lease:        lease,
		Registry:     h.reg,
	})
	if err == nil && srv.Owned() {
		h.ownedServers = append(h.ownedServers, srv)
	}
	return srv, err
}

// stopOwnedServers stops every server this invocation started.
func (h *harness) stopOwnedServers() {
	for _, s := range h.ownedServers {
		s.Stop()
	}
	h.ownedServers = nil
}

// reclaimResource releases one owned resource through its provider.
func (h *harness) reclaimResource(ctx context.Context, r *process.Resource) error {
	switch r.Kind {
	case process.KindBrowser:
		p := browser.NewAgentBrowser(h.cfg, r.Namespace)
		return p.Close(ctx)
	case process.KindServer:
		pgid := 0
		if v := r.Metadata["pgid"]; v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				pgid = n
			}
		}
		// Only a process Game Forge registered is terminated, by pid/pgid.
		server.KillProcessGroup(r.PID, pgid)
		return nil
	default:
		return fmt.Errorf("no reclaimer for kind %q", r.Kind)
	}
}
