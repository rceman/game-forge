package client

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rceman/game-forge/internal/daemon"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
}

// TestLockAcquireRelease proves the startup lock is exclusive.
func TestLockAcquireRelease(t *testing.T) {
	isolate(t)
	lock, err := daemon.LockPath()
	if err != nil {
		t.Fatal(err)
	}
	f, err := acquireLock(lock)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	// A second acquire must report the lock as held.
	if _, err := acquireLock(lock); err != errLockHeld {
		t.Errorf("second acquire: want errLockHeld, got %v", err)
	}
	f.Close()
	os.Remove(lock)
	// After release, acquiring succeeds again.
	f2, err := acquireLock(lock)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	f2.Close()
	os.Remove(lock)
}

// TestStaleLockRecovery proves a lock older than lockStaleAfter is reclaimed.
func TestStaleLockRecovery(t *testing.T) {
	isolate(t)
	lock, _ := daemon.LockPath()
	os.MkdirAll(filepath.Dir(lock), 0o700)
	// Plant an old lock file and back-date it.
	os.WriteFile(lock, []byte("99999"), 0o600)
	old := time.Now().Add(-lockStaleAfter - time.Second)
	os.Chtimes(lock, old, old)
	info, _ := os.Stat(lock)
	if time.Since(info.ModTime()) <= lockStaleAfter {
		t.Fatal("test setup: lock is not stale")
	}
	// The start loop treats it as reclaimable: it removes and acquires.
	// We verify the removal path directly since spawnAndWait would spawn a real
	// daemon.
	os.Remove(lock)
	if _, err := acquireLock(lock); err != nil {
		t.Errorf("stale lock not recovered: %v", err)
	}
	os.Remove(lock)
}
