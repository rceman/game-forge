package core

import (
	"context"
	"testing"
	"time"

	"github.com/rceman/game-forge/internal/process"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
}

const deadPID = 4194303 // a pid that does not exist, so SIGTERM is a no-op

// TestTickSkipsActiveRuns proves housekeeping never reclaims a resource whose
// owning run is still in-flight, but does reclaim an expired orphan.
func TestTickSkipsActiveRuns(t *testing.T) {
	isolate(t)
	rt := NewRuntime(false)
	c, err := New(rt, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	past := time.Now().UTC().Add(-time.Hour)
	// Owned by this Core's (active) run.
	if err := c.reg.Register(&process.Resource{
		ID: "s-active", RunID: c.RunID(), Kind: process.KindServer, Provider: "local",
		PID: deadPID, Expires: past, Metadata: map[string]string{"pgid": "0"},
	}); err != nil {
		t.Fatal(err)
	}
	// Owned by a run that is not in-flight.
	if err := c.reg.Register(&process.Resource{
		ID: "s-orphan", RunID: "gone", Kind: process.KindServer, Provider: "local",
		PID: deadPID, Expires: past, Metadata: map[string]string{"pgid": "0"},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := c.tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 {
		t.Errorf("unexpected reclaim failures: %d", res.Failed)
	}
	// The orphan was reclaimed; the active resource was not.
	reclaimed := map[string]bool{}
	for _, r := range res.Reclaimed {
		reclaimed[r.ID] = true
	}
	if !reclaimed["s-orphan"] {
		t.Error("orphan should have been reclaimed")
	}
	if reclaimed["s-active"] {
		t.Error("active resource must not be reclaimed")
	}
	// The active record is still registered.
	all, _ := c.reg.List()
	found := false
	for _, r := range all {
		if r.ID == "s-active" {
			found = true
		}
	}
	if !found {
		t.Error("active resource record was wrongly removed")
	}
}

// TestTickIdempotent proves a second pass finds nothing to do.
func TestTickIdempotent(t *testing.T) {
	isolate(t)
	rt := NewRuntime(false)
	c, err := New(rt, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	past := time.Now().UTC().Add(-time.Hour)
	c.reg.Register(&process.Resource{
		ID: "s1", RunID: "gone", Kind: process.KindServer, Provider: "local",
		PID: deadPID, Expires: past, Metadata: map[string]string{"pgid": "0"},
	})
	res1, _ := c.tick(context.Background())
	if len(res1.Reclaimed) != 1 {
		t.Fatalf("first tick should reclaim 1, got %d", len(res1.Reclaimed))
	}
	res2, _ := c.tick(context.Background())
	if len(res2.Reclaimed) != 0 {
		t.Errorf("second tick should reclaim nothing, got %d", len(res2.Reclaimed))
	}
}

// TestRunTracking proves beginRun/Close mark and clear an active run.
func TestRunTracking(t *testing.T) {
	isolate(t)
	rt := NewRuntime(false)
	c, err := New(rt, "")
	if err != nil {
		t.Fatal(err)
	}
	if !rt.ActiveRuns()[c.RunID()] {
		t.Error("run should be active while the Core is open")
	}
	c.Close()
	if rt.ActiveRuns()[c.RunID()] {
		t.Error("run should be inactive after Close")
	}
}
