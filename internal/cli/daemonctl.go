package cli

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/rceman/game-forge/internal/client"
	"github.com/rceman/game-forge/internal/config"
	"github.com/rceman/game-forge/internal/core"
	"github.com/rceman/game-forge/internal/daemon"
	"github.com/rceman/game-forge/internal/lifecycle"
	"github.com/rceman/game-forge/internal/mcpfrontend"
	"github.com/rceman/game-forge/internal/op"
	"github.com/rceman/game-forge/internal/service"
)

// cmdDaemon owns the administrative daemon commands. Human-facing lifecycle
// (start/stop/restart/status) delegates to the same lifecycle implementation
// as the top-level commands.
func cmdDaemon(args []string) int {
	sub := first(args)
	switch sub {
	case "serve":
		return daemonServe()
	case "install":
		return daemonInstall()
	case "uninstall":
		return daemonUninstall()
	case "rebind":
		return daemonRebind()
	case "start":
		return cmdStart()
	case "status":
		return cmdStatus()
	case "stop":
		return cmdStop()
	case "restart":
		return cmdRestart()
	default:
		fmt.Fprintln(os.Stderr, "game-forge daemon: expected subcommand (serve|install|uninstall|rebind|start|stop|restart|status)")
		return ExitUsage
	}
}

// daemonServe runs the daemon worker in-process. Under a service manager it
// is the service's foreground process; detached fallback spawns it directly.
func daemonServe() int {
	// Singleton guard: if a healthy daemon already answers, converge rather
	// than bind a second port. A daemon that is shutting down is handled by
	// Serve's lifetime-lock wait, not here.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if client.Connect(ctx) != nil {
		cancel()
		return ExitOK
	}
	cancel()
	rt := core.NewRuntime(true)
	reg := op.NewRegistry()
	if err := core.Register(reg, rt); err != nil {
		fmt.Fprintf(os.Stderr, "game-forged: %v\n", err)
		return ExitFail
	}
	var logger *log.Logger
	if logPath, err := daemon.LogPath(); err == nil {
		if err := os.MkdirAll(dirOf(logPath), 0o755); err == nil {
			if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
				defer f.Close()
				logger = log.New(f, "game-forged ", log.LstdFlags|log.Lmsgprefix)
			}
		}
	}
	srv := daemon.NewServer(rt, reg, logger)
	// Mount the canonical MCP endpoint on the daemon's one listener. The
	// frontend shares this server's in-process dispatcher — /mcp calls run
	// the same pipeline as /v1/run, and the daemon package stays free of the
	// frontend (no import cycle).
	mcpfrontend.Version = Version
	var mcpOpt mcpfrontend.Options
	if cfg, _, err := config.LoadDefault(); err == nil {
		// mcp.compat_text is the machine-level opt-in for MCP clients that
		// consume content but not structuredContent (e.g. this harness).
		mcpOpt.CompatText = cfg.MCP.CompatText
	}
	mcpH, err := mcpfrontend.HTTPHandler(srv, mcpOpt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forged: mount MCP: %v\n", err)
		return ExitFail
	}
	srv.SetMCP(mcpH)
	if err := srv.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "game-forged: %v\n", err)
		return ExitFail
	}
	return ExitOK
}

// daemonInstall registers Game Forge with the native per-user service manager
// (systemd --user on Linux/WSL) and starts it under service ownership.
func daemonInstall() int {
	m := service.Current()
	if err := m.Available(); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge daemon install: %v\n", err)
		return ExitFail
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge daemon install: %v\n", err)
		return ExitFail
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	// Transition cleanly: stop whatever incarnation is running — detached or
	// service-managed alike — before systemd takes ownership, so the two
	// never coexist.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if cl := client.Connect(ctx); cl != nil {
		if err := cl.Shutdown(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "game-forge daemon install: %v\n", err)
			return ExitFail
		}
		if err := lifecycle.WaitGone(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "game-forge daemon install: %v\n", err)
			return ExitFail
		}
	}
	if err := m.Install(exe); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge daemon install: %v\n", err)
		return ExitFail
	}
	if _, err := lifecycle.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge daemon install: %v\n", err)
		return ExitFail
	}
	fmt.Printf("game-forge service installed\n  service:  %s\n  unit:     %s\n  exec:     %s daemon serve\n", m.Name(), service.Unit, exe)
	return ExitOK
}

// daemonUninstall removes the service registration. Durable Game Forge state
// — project registry, stable port, MCP credential — is preserved.
func daemonUninstall() int {
	m := service.Current()
	if err := m.Available(); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge daemon uninstall: %v\n", err)
		return ExitFail
	}
	if !m.Installed() {
		fmt.Println("game-forge service not installed")
		return ExitOK
	}
	if err := m.Uninstall(); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge daemon uninstall: %v\n", err)
		return ExitFail
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if cl := client.Connect(ctx); cl != nil {
		// A manually spawned daemon may outlive the unit; stop it too.
		if err := lifecycle.Stop(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "game-forge daemon uninstall: %v\n", err)
			return ExitFail
		}
	}
	fmt.Println("game-forge service uninstalled (state preserved)")
	return ExitOK
}

// daemonRebind is the explicit recovery for an occupied durable port: stop
// the daemon, drop the persisted port, and start fresh on a newly chosen one.
// MCP client configuration must then use the new endpoint — the port never
// moves silently.
func daemonRebind() int {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if client.Connect(ctx) != nil || lifecycle.Installed() {
		if err := lifecycle.Stop(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "game-forge daemon rebind: %v\n", err)
			return ExitFail
		}
	}
	if err := daemon.ClearEndpoint(); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge daemon rebind: %v\n", err)
		return ExitFail
	}
	cl, err := lifecycle.Start(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge daemon rebind: %v\n", err)
		return ExitFail
	}
	d := cl.Discovery()
	fmt.Printf("game-forged rebound\n  endpoint: %s\n  mcp:      %s/mcp\n", d.Endpoint, d.Endpoint)
	fmt.Println("  update MCP client configuration to the new endpoint")
	return ExitOK
}

// dirOf returns the directory part of a path without importing filepath again.
func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}
