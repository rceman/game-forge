package project

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// mkProject writes a minimal valid manifest under a fresh temp dir.
func mkProject(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	manifest := "contract: game-forge/v1\nproject:\n  id: " + id + "\n"
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	r, err := OpenRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNormalizeCode(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"TDG", "TDG", true},
		{"tdg", "TDG", true}, // case-normalized to uppercase
		{"test2", "TEST2", true},
		{"MY-PROJ", "MY-PROJ", true},
		{"my_proj", "MY_PROJ", true},
		{"", "", false},                      // empty
		{"  ", "", false},                    // whitespace
		{"a b", "", false},                   // interior space
		{"a/b", "", false},                   // path-like
		{"a\\b", "", false},                  // windows path-like
		{"../x", "", false},                  // traversal
		{"a.b", "", false},                   // dotted
		{"a:b", "", false},                   // drive-like
		{strings.Repeat("A", 17), "", false}, // too long
		{strings.Repeat("A", 16), strings.Repeat("A", 16), true},
	} {
		got, err := NormalizeCode(tc.in)
		if tc.ok && err != nil {
			t.Errorf("NormalizeCode(%q) failed: %v", tc.in, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("NormalizeCode(%q) accepted %q", tc.in, got)
		}
		if tc.ok && got != tc.want {
			t.Errorf("NormalizeCode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRegistryAddShowListRemove(t *testing.T) {
	r := testRegistry(t)
	dirA := mkProject(t, "proj-a")
	dirB := mkProject(t, "proj-b")

	rec, err := r.Add("tdg", dirA, false)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Code != "TDG" {
		t.Fatalf("code not normalized: %q", rec.Code)
	}
	if rec.Root != CanonicalRoot(dirA) || rec.ProjectID != "proj-a" || rec.ProjectKey == "" {
		t.Fatalf("bad registration: %+v", rec)
	}

	// Nested directory resolves to the manifest root.
	sub := filepath.Join(dirB, "src", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	recB, err := r.Add("BBB", sub, false)
	if err != nil {
		t.Fatal(err)
	}
	if recB.Root != CanonicalRoot(dirB) {
		t.Fatalf("nested add did not resolve to manifest root: %q", recB.Root)
	}

	list := r.List()
	if len(list) != 2 || list[0].Code != "BBB" || list[1].Code != "TDG" {
		t.Fatalf("list wrong: %+v", list)
	}
	if got, ok := r.Get("bbb"); !ok || got.ProjectKey != recB.ProjectKey {
		t.Fatalf("get(bbb) failed: %+v", got)
	}

	if err := r.Remove("TDG"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get("TDG"); ok {
		t.Fatal("removed project still resolves")
	}
	if err := r.Remove("TDG"); err == nil {
		t.Fatal("removing an unknown code should fail")
	}
}

func TestRegistryDuplicateNeedsReplace(t *testing.T) {
	r := testRegistry(t)
	dirA := mkProject(t, "proj-a")
	dirB := mkProject(t, "proj-b")
	if _, err := r.Add("AAA", dirA, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add("AAA", dirB, false); err == nil {
		t.Fatal("duplicate code without --replace must fail")
	}
	rec, err := r.Add("AAA", dirB, true)
	if err != nil {
		t.Fatal(err)
	}
	if rec.ProjectID != "proj-b" {
		t.Fatalf("--replace did not update mapping: %+v", rec)
	}
}

func TestRegistryResolveStaleness(t *testing.T) {
	r := testRegistry(t)
	dir := mkProject(t, "proj-a")
	if _, err := r.Add("AAA", dir, false); err != nil {
		t.Fatal(err)
	}
	root, lerr := r.Resolve("AAA")
	if lerr != nil || root != CanonicalRoot(dir) {
		t.Fatalf("resolve failed: %v %q", lerr, root)
	}

	// Manifest replaced by a different project id → project_changed, not a
	// silent redirect.
	manifest := "contract: game-forge/v1\nproject:\n  id: different\n"
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, lerr := r.Resolve("AAA"); lerr == nil || lerr.Code != "project_changed" {
		t.Fatalf("expected project_changed, got %v", lerr)
	}

	// Directory gone → project_unavailable.
	dirGone := mkProject(t, "proj-gone")
	if _, err := r.Add("GONE", dirGone, false); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dirGone); err != nil {
		t.Fatal(err)
	}
	if _, lerr := r.Resolve("GONE"); lerr == nil || lerr.Code != "project_unavailable" {
		t.Fatalf("expected project_unavailable, got %v", lerr)
	}
	if _, lerr := r.Resolve("NOPE"); lerr == nil || lerr.Code != "unknown_project" {
		t.Fatalf("expected unknown_project, got %v", lerr)
	}
}

func TestRegistryConcurrentWriters(t *testing.T) {
	r := testRegistry(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dir := mkProject(t, "proj")
			if _, err := r.Add("P"+string(rune('A'+i)), dir, false); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if n := len(r.List()); n != 8 {
		t.Fatalf("concurrent adds lost entries: %d", n)
	}
}
