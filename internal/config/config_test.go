package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Version != CurrentVersion {
		t.Errorf("version = %d, want %d", c.Version, CurrentVersion)
	}
	if c.Browser.Provider != "agent-browser" {
		t.Errorf("provider = %q, want agent-browser", c.Browser.Provider)
	}
	if !c.Browser.AgentBrowser.Headless {
		t.Error("default should be headless")
	}
}

func TestLoadMissingReturnsDefault(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Browser.Provider != "agent-browser" {
		t.Errorf("provider = %q, want default", c.Browser.Provider)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	c := Default()
	c.Browser.Host = "windows"
	c.Browser.AgentBrowser.CLI = `C:\tools\agent-browser.cmd`
	c.Browser.AgentBrowser.Chrome = `C:\Program Files\Google\Chrome\Application\chrome.exe`
	if err := c.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Browser.Host != "windows" {
		t.Errorf("host = %q, want windows", got.Browser.Host)
	}
	if got.Browser.AgentBrowser.CLI != `C:\tools\agent-browser.cmd` {
		t.Errorf("cli = %q", got.Browser.AgentBrowser.CLI)
	}
	if !got.Browser.AgentBrowser.Headless {
		t.Error("headless should persist as true")
	}
}

func TestLoadRejectsUnsupportedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load with version 99 = nil error, want error")
	}
}

func TestDirHonoursEnvOverride(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", "/tmp/gf-home-test")
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != "/tmp/gf-home-test" {
		t.Errorf("Dir = %q, want override", dir)
	}
}
