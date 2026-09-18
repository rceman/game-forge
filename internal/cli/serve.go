package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/rceman/game-forge/internal/process"
	"github.com/rceman/game-forge/internal/project"
	"github.com/rceman/game-forge/internal/server"
)

// cmdServe owns the lifecycle of a project's declared dev/prod servers.
//
// The project declares the command, the readiness URL and the timeout; Game
// Forge owns starting, readiness, reuse, process identity, resource
// registration, stopping and stale-resource recovery. Nothing here matches
// processes by name: only a server this registry owns is ever terminated.
func cmdServe(args []string) int {
	fs := newFlagSet("serve")
	kind := fs.String("server", "dev", "server to manage (dev|prod)")
	lease := fs.Duration("lease", 30*time.Minute, "how long an abandoned server stays reclaimable")
	timeout := fs.Duration("timeout", 3*time.Minute, "overall timeout")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if code, done := fs.parse(args); done {
		return code
	}

	action := "start"
	if rest := fs.Args(); len(rest) > 0 {
		action = rest[0]
	}

	h, err := newHarness()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge serve: %v\n", err)
		return ExitFail
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	switch action {
	case "start", "status", "stop":
	default:
		fmt.Fprintf(os.Stderr, "game-forge serve: unknown action %q (start|status|stop)\n", action)
		return ExitUsage
	}
	if *kind != "dev" && *kind != "prod" {
		fmt.Fprintf(os.Stderr, "game-forge serve: unknown server %q (dev|prod)\n", *kind)
		return ExitUsage
	}

	if action == "start" {
		return serveStart(ctx, h, *kind, *lease, *jsonOut)
	}
	return serveReport(h, *kind, action == "stop", *jsonOut)
}

// serveStart ensures the declared server is running and leaves it running.
//
// A served server deliberately outlives this command: later commands reuse it
// through its readiness URL. It stays reclaimable through its lease, so an
// abandoned server is recovered by gc/tick rather than leaking forever.
func serveStart(ctx context.Context, h *harness, kind string, lease time.Duration, jsonOut bool) int {
	spec, err := h.serverSpec(kind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge serve: %v\n", err)
		return ExitFail
	}
	srv, err := server.Ensure(ctx, server.Options{
		Name:         kind,
		Command:      spec.Command,
		URL:          spec.URL,
		ReadyTimeout: spec.ReadyTimeout.Std(),
		Dir:          h.m.Root,
		LogDir:       h.logDir,
		RunID:        h.runID,
		Project:      h.m.Project.ID,
		Lease:        lease,
		Registry:     h.reg,
		Stderr:       os.Stderr,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge serve: %v\n", err)
		return ExitFail
	}

	state := "reused"
	if srv.Owned() {
		state = "started"
	}
	if jsonOut {
		return emitJSON(map[string]any{
			"server": kind,
			"state":  state,
			"url":    srv.URL(),
			"pid":    srv.PID(),
			"log":    srv.LogPath(),
			"lease":  lease.String(),
		})
	}
	fmt.Printf("server=%s state=%s url=%s\n", kind, state, srv.URL())
	if srv.Owned() {
		fmt.Printf("pid=%d log=%s lease=%s\n", srv.PID(), srv.LogPath(), lease)
		fmt.Println("registered as an owned resource; stop it with 'game-forge serve stop'")
	} else {
		fmt.Println("already reachable, so it was left alone (not owned by this run)")
	}
	return ExitOK
}

// serveReport lists declared servers, and with stop also terminates the ones
// this registry owns.
func serveReport(h *harness, kind string, stop bool, jsonOut bool) int {
	records, err := h.reg.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge serve: %v\n", err)
		return ExitFail
	}

	type entry struct {
		Server string `json:"server"`
		URL    string `json:"url,omitempty"`
		Owned  bool   `json:"owned"`
		PID    int    `json:"pid,omitempty"`
		ID     string `json:"id,omitempty"`
		State  string `json:"state,omitempty"`
	}

	var out []entry
	failed := false
	for _, name := range []string{kind} {
		e := entry{Server: name}
		if spec, err := h.serverSpec(name); err == nil {
			e.URL = spec.URL
		}
		for _, r := range records {
			if r.Kind != process.KindServer || r.Metadata["name"] != name {
				continue
			}
			e.Owned = true
			e.PID = r.PID
			e.ID = r.ID
			if stop {
				server.KillProcessGroup(r.PID, atoiOr(r.Metadata["pgid"]))
				if err := h.reg.Remove(r.ID); err != nil {
					fmt.Fprintf(os.Stderr, "  ! remove record: %v\n", err)
					failed = true
				}
				e.Owned = false
				e.State = "stopped"
				continue
			}
			e.State = "owned"
		}
		if !e.Owned && e.State == "" {
			if e.URL != "" && server.Reachable(context.Background(), e.URL, 1500*time.Millisecond) {
				e.State = "reachable, not owned by game-forge"
			} else {
				e.State = "not running"
			}
		}
		out = append(out, e)
	}

	if jsonOut {
		return emitJSON(out)
	}
	for _, e := range out {
		fmt.Printf("server=%s state=%s url=%s\n", e.Server, e.State, e.URL)
		if e.PID > 0 {
			fmt.Printf("  id=%s pid=%d\n", e.ID, e.PID)
		}
	}
	if failed {
		return ExitFail
	}
	return ExitOK
}

// serverSpec returns the declared server of kind, or an error.
func (h *harness) serverSpec(kind string) (project.Server, error) {
	switch kind {
	case "dev":
		if h.m.Server.Dev.URL == "" {
			return project.Server{}, fmt.Errorf("project declares no dev server")
		}
		return h.m.Server.Dev, nil
	case "prod":
		if h.m.Server.Prod.URL == "" {
			return project.Server{}, fmt.Errorf("project declares no prod server")
		}
		return h.m.Server.Prod, nil
	}
	return project.Server{}, fmt.Errorf("unknown server %q", kind)
}

func atoiOr(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
