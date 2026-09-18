package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points GAME_FORGE_HOME at a temp dir so discovery never touches the
// real ~/.game-forge.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
}

func TestDiscoveryRoundTrip(t *testing.T) {
	isolate(t)
	d := &Discovery{Protocol: Protocol, Endpoint: "http://127.0.0.1:9999", PID: 123, Token: "tok"}
	if err := Write(d); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := Read()
	if err != nil || got == nil {
		t.Fatalf("read: %v nil=%v", err, got == nil)
	}
	if got.Endpoint != d.Endpoint || got.PID != d.PID || got.Token != d.Token {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestDiscoveryPermissions(t *testing.T) {
	isolate(t)
	d := &Discovery{Protocol: Protocol, Endpoint: "http://127.0.0.1:1", PID: 1, Token: "t"}
	if err := Write(d); err != nil {
		t.Fatal(err)
	}
	path, _ := DiscoveryPath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Owner-only: no group/other bits.
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("discovery is not owner-only: %o", info.Mode().Perm())
	}
}

func TestDiscoveryMalformed(t *testing.T) {
	isolate(t)
	dir, _ := RunDir()
	os.MkdirAll(dir, 0o700)
	path, _ := DiscoveryPath()
	os.WriteFile(path, []byte("{not json"), 0o600)
	got, err := Read()
	if err != nil {
		t.Fatalf("malformed discovery should not error: %v", err)
	}
	if got != nil {
		t.Error("malformed discovery should read as nil (stale)")
	}
}

func TestDiscoveryWrongProtocol(t *testing.T) {
	isolate(t)
	d := &Discovery{Protocol: "other/v9", Endpoint: "http://x", PID: 1, Token: "t"}
	if err := Write(d); err != nil {
		t.Fatal(err)
	}
	if got, _ := Read(); got != nil {
		t.Error("wrong protocol should read as nil (stale)")
	}
}

func TestDiscoveryAtomic(t *testing.T) {
	isolate(t)
	// Write twice in rapid succession; the file must always be a complete
	// document, never a partial write.
	d := &Discovery{Protocol: Protocol, Endpoint: "http://127.0.0.1:2", PID: 1, Token: "t"}
	for i := 0; i < 20; i++ {
		d.PID = i + 1
		if err := Write(d); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		got, err := Read()
		if err != nil || got == nil {
			t.Fatalf("iteration %d produced unreadable discovery", i)
		}
	}
	// No leftover temp files.
	dir, _ := RunDir()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "daemon-") && strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestDiscoveryRemove(t *testing.T) {
	isolate(t)
	d := &Discovery{Protocol: Protocol, Endpoint: "http://127.0.0.1:1", PID: 1, Token: "t"}
	Write(d)
	if err := Remove(); err != nil {
		t.Fatal(err)
	}
	if got, _ := Read(); got != nil {
		t.Error("removed discovery should read as nil")
	}
	// Removing again is not an error.
	if err := Remove(); err != nil {
		t.Error("remove should be idempotent")
	}
}

func TestDiscoveryPathUnderHome(t *testing.T) {
	isolate(t)
	path, err := DiscoveryPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(filepath.ToSlash(path), "/run/daemon.json") {
		t.Errorf("unexpected discovery path %s", path)
	}
}

// TestProcessAlive covers the liveness probe.
func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("current process should be alive")
	}
	if processAlive(0) {
		t.Error("pid 0 should not be alive")
	}
}
