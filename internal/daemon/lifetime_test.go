package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// writeOwnedLock plants a daemon-owned.lock carrying the given pid.
func writeOwnedLock(t *testing.T, pid int) string {
	t.Helper()
	path, err := OwnedLockPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLifetimeLockWaitsForDrainingPredecessor is the deterministic form of the
// observed restart race: a predecessor still owns the lock while it drains —
// a new incarnation must wait for release, not fail instantly.
func TestLifetimeLockWaitsForDrainingPredecessor(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	// A live placeholder pid: this test process is guaranteed alive.
	lockPath := writeOwnedLock(t, os.Getpid())

	done := make(chan error, 1)
	go func() {
		release, err := acquireLifetimeLock()
		if err == nil {
			release()
		}
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("acquire returned while a live predecessor owned the lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	os.Remove(lockPath)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("acquire after release: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("acquire did not proceed after the predecessor released ownership")
	}
}

// TestLifetimeLockReclaimsDeadOwner covers the crashed-daemon path: a lock
// file whose pid is gone is stale and reclaimed immediately.
func TestLifetimeLockReclaimsDeadOwner(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("sleep unavailable: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	writeOwnedLock(t, pid)

	release, err := acquireLifetimeLock()
	if err != nil {
		t.Fatalf("stale lock should be reclaimed: %v", err)
	}
	release()
}

func TestOwnershipFree(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	if !OwnershipFree() {
		t.Fatal("no lock file should mean ownership free")
	}
	lockPath := writeOwnedLock(t, os.Getpid())
	if OwnershipFree() {
		t.Fatal("live owner pid must mean ownership held")
	}
	os.Remove(lockPath)
	if !OwnershipFree() {
		t.Fatal("removed lock should mean ownership free")
	}
}
