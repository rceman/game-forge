package cli

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/rceman/game-forge/internal/client"
	"github.com/rceman/game-forge/internal/core"
	"github.com/rceman/game-forge/internal/daemon"
	"github.com/rceman/game-forge/internal/mcpfrontend"
	"github.com/rceman/game-forge/internal/op"
)

// cmdDaemon owns the daemon lifecycle commands. These are the only
// bootstrap-level commands: everything else goes through the daemon.
func cmdDaemon(args []string) int {
	sub := first(args)
	switch sub {
	case "serve":
		return daemonServe()
	case "start":
		return daemonStart()
	case "status":
		return daemonStatus()
	case "stop":
		return daemonStop()
	case "restart":
		return daemonRestart()
	case "rebind":
		return daemonRebind()
	default:
		fmt.Fprintln(os.Stderr, "game-forge daemon: expected subcommand (start|status|stop|restart|rebind)")
		return ExitUsage
	}
}

// daemonServe runs the daemon worker in-process. It is the process the CLI
// spawns; it is not meant to be run by hand.
func daemonServe() int {
	// Singleton guard: if a healthy daemon already answers, exit rather than
	// bind a second port and take over discovery.
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
	mcpH, err := mcpfrontend.HTTPHandler(srv, mcpfrontend.Options{})
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

// daemonStart ensures the daemon is running and reports it.
func daemonStart() int {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cl, err := client.Ensure(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge daemon start: %v\n", err)
		return ExitFail
	}
	d := cl.Discovery()
	fmt.Printf("game-forged running\n  endpoint: %s\n  pid:      %d\n", d.Endpoint, d.PID)
	return ExitOK
}

// daemonStatus reports whether the daemon is running.
func daemonStatus() int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl := client.Connect(ctx)
	if cl == nil {
		fmt.Println("game-forged: not running")
		return ExitOK
	}
	d := cl.Discovery()
	ops, _ := cl.Capabilities(ctx)
	fmt.Printf("game-forged running\n  endpoint: %s\n  pid:      %d\n  ops:      %d\n  mcp:      %s/mcp\n", d.Endpoint, d.PID, len(ops), d.Endpoint)
	return ExitOK
}

// daemonRebind is the explicit recovery for an occupied durable port: stop
// the daemon, drop the persisted port, and start fresh on a newly chosen one.
// MCP client configuration must then use the new endpoint — the port never
// moves silently.
func daemonRebind() int {
	if client.Connect(context.Background()) != nil {
		if code := daemonStop(); code != ExitOK {
			return code
		}
	}
	if err := daemon.ClearEndpoint(); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge daemon rebind: %v\n", err)
		return ExitFail
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cl, err := client.Ensure(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge daemon rebind: %v\n", err)
		return ExitFail
	}
	d := cl.Discovery()
	fmt.Printf("game-forged rebound\n  endpoint: %s\n  mcp:      %s/mcp\n", d.Endpoint, d.Endpoint)
	fmt.Println("  update MCP client configuration to the new endpoint")
	return ExitOK
}

// daemonStop gracefully stops the daemon and confirms discovery is gone.
func daemonStop() int {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cl := client.Connect(ctx)
	if cl == nil {
		fmt.Println("game-forged: not running")
		return ExitOK
	}
	if err := cl.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge daemon stop: %v\n", err)
		return ExitFail
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if client.Connect(ctx) == nil {
			fmt.Println("game-forged stopped")
			return ExitOK
		}
		time.Sleep(150 * time.Millisecond)
	}
	fmt.Println("game-forged stopped")
	return ExitOK
}

// daemonRestart stops then starts the daemon.
func daemonRestart() int {
	if code := daemonStop(); code != ExitOK {
		return code
	}
	return daemonStart()
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
