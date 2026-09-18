package project

import (
	"os"
	"path/filepath"
	"testing"
)

const validManifest = `contract: game-forge/v1
project:
  id: spin-tower
  type: web-game
capabilities:
  simulation: true
  browser: true
adapters:
  simulation:
    command: [npx, tsx, scripts/simulate.ts]
    protocol: jsonl
`

func writeManifest(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverWalksUp(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, validManifest)
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(deep)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// Resolve symlinks (macOS temp dirs) before comparing.
	wantRoot, _ := filepath.EvalSymlinks(root)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantRoot {
		t.Errorf("Discover = %q, want %q", gotResolved, wantRoot)
	}
}

func TestDiscoverMissing(t *testing.T) {
	if _, err := Discover(t.TempDir()); err == nil {
		t.Fatal("Discover with no manifest = nil error, want error")
	}
}

func TestLoadAndValidate(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, validManifest)
	m, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Project.ID != "spin-tower" {
		t.Errorf("id = %q", m.Project.ID)
	}
	if !m.Supports("browser") {
		t.Error("browser capability should be enabled")
	}
	if m.Supports("audio") {
		t.Error("audio capability should be absent")
	}
	if len(m.Adapters["simulation"].Command) != 3 {
		t.Errorf("adapter command = %v", m.Adapters["simulation"].Command)
	}
}

func TestValidateRejectsBadManifest(t *testing.T) {
	cases := map[string]string{
		"bad contract": "contract: nope\nproject:\n  id: x\n",
		"no id":        "contract: game-forge/v1\nproject: {}\n",
		"no command":   "contract: game-forge/v1\nproject:\n  id: x\nadapters:\n  simulation:\n    protocol: jsonl\n",
		"no protocol":  "contract: game-forge/v1\nproject:\n  id: x\nadapters:\n  simulation:\n    command: [run]\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeManifest(t, root, body)
			if _, err := Load(root); err == nil {
				t.Fatalf("Load(%s) = nil error, want error", name)
			}
		})
	}
}
