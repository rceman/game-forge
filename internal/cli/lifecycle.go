package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/rceman/game-forge/internal/lifecycle"
)

// cmdStart ensures Game Forge is running and reports it.
func cmdStart() int {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cl, err := lifecycle.Start(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge start: %v\n", err)
		return ExitFail
	}
	d := cl.Discovery()
	fmt.Printf("Game Forge running\n  endpoint: %s\n  pid:      %d\n", d.Endpoint, d.PID)
	return ExitOK
}

// cmdStop stops Game Forge cleanly.
func cmdStop() int {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := lifecycle.Stop(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge stop: %v\n", err)
		return ExitFail
	}
	fmt.Println("Game Forge stopped")
	return ExitOK
}

// cmdRestart replaces the running incarnation cleanly.
func cmdRestart() int {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cl, err := lifecycle.Restart(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge restart: %v\n", err)
		return ExitFail
	}
	d := cl.Discovery()
	fmt.Printf("Game Forge running\n  endpoint: %s\n  pid:      %d\n", d.Endpoint, d.PID)
	return ExitOK
}

// cmdStatus reports lifecycle state. It never prints credentials.
func cmdStatus() int {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st := lifecycle.Stat(ctx)
	if !st.Running {
		fmt.Printf("Game Forge stopped\n  service:  %s\n", st.Service)
		return ExitOK
	}
	fmt.Printf("Game Forge running\n  pid:      %d\n  endpoint: %s\n  mcp:      %s/mcp\n  service:  %s\n",
		st.PID, st.Endpoint, st.Endpoint, st.Service)
	return ExitOK
}
