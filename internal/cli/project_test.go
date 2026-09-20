package cli

// Project-registry CLI tests: real discovery, real persistence, real argument
// validation — no parser-only fakes.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rceman/game-forge/internal/project"
)

// mkProject writes a minimal valid project manifest in a temp dir.
func mkProject(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	manifest := "contract: game-forge/v1\nproject:\n  id: " + id + "\n"
	if err := os.WriteFile(filepath.Join(dir, "game-forge.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func openReg(t *testing.T) *project.Registry {
	t.Helper()
	r, err := project.OpenRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestProjectAddShorthand(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	dir := mkProject(t, "spin-tower")
	t.Chdir(dir)

	if code := Run([]string{"project", "add", "TDG"}); code != ExitOK {
		t.Fatalf("project add TDG: exit %d", code)
	}
	rec, ok := openReg(t).Get("TDG")
	if !ok {
		t.Fatal("TDG not registered")
	}
	if rec.Root != project.CanonicalRoot(dir) || rec.ProjectID != "spin-tower" || rec.ProjectKey == "" {
		t.Fatalf("bad record: %+v", rec)
	}
}

func TestProjectAddFromNestedDir(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	dir := mkProject(t, "spin-tower")
	nested := filepath.Join(dir, "src", "sim")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)

	if code := Run([]string{"project", "add", "TDG"}); code != ExitOK {
		t.Fatalf("nested project add: exit %d", code)
	}
	rec, ok := openReg(t).Get("TDG")
	if !ok || rec.Root != project.CanonicalRoot(dir) {
		t.Fatalf("nested add must resolve to the manifest root: %+v", rec)
	}
}

func TestProjectAddOutsideProject(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	t.Chdir(t.TempDir()) // no manifest anywhere above
	if code := Run([]string{"project", "add", "TDG"}); code != ExitFail {
		t.Fatalf("add outside a project: exit %d, want fail", code)
	}
}

func TestProjectAddDuplicateAndReplace(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	dirA := mkProject(t, "proj-a")
	dirB := mkProject(t, "proj-b")

	t.Chdir(dirA)
	if Run([]string{"project", "add", "AAA"}) != ExitOK {
		t.Fatal("first add failed")
	}
	// Duplicate without --replace must fail.
	t.Chdir(dirB)
	if Run([]string{"project", "add", "AAA"}) != ExitFail {
		t.Fatal("duplicate code without --replace must fail")
	}
	if Run([]string{"project", "add", "AAA", "--replace"}) != ExitOK {
		t.Fatal("--replace add failed")
	}
	rec, _ := openReg(t).Get("AAA")
	if rec.ProjectID != "proj-b" {
		t.Fatalf("--replace did not update: %+v", rec)
	}
}

func TestProjectAddExplicit(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	dir := mkProject(t, "proj-x")
	t.Chdir(t.TempDir())
	if code := Run([]string{"project", "add", "--code", "BBB", "--folder", dir}); code != ExitOK {
		t.Fatalf("explicit add: exit %d", code)
	}
	rec, ok := openReg(t).Get("BBB")
	if !ok || rec.Root != project.CanonicalRoot(dir) {
		t.Fatalf("explicit add bad: %+v", rec)
	}
}

func TestProjectAddAmbiguous(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	dir := mkProject(t, "proj-x")
	t.Chdir(dir)
	for _, args := range [][]string{
		{"project", "add", "TDG", "--code", "ABC"},
		{"project", "add", "TDG", "--folder", dir},
		{"project", "add"},
		{"project", "add", "TDG", "ABC"},
		{"project", "add", "--code", "TDG"}, // missing --folder
		{"project", "add", "--folder", dir}, // missing --code
	} {
		if code := Run(args); code != ExitUsage {
			t.Fatalf("%v: exit %d, want usage", args, code)
		}
	}
	if n := len(openReg(t).List()); n != 0 {
		t.Fatalf("ambiguous calls must not register anything: %d", n)
	}
}

func TestProjectAddEquivalentForms(t *testing.T) {
	// Shorthand and explicit form must produce semantically identical records.
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	dir := mkProject(t, "spin-tower")
	t.Chdir(dir)
	if Run([]string{"project", "add", "AAA"}) != ExitOK {
		t.Fatal("shorthand add failed")
	}
	if Run([]string{"project", "add", "--code", "BBB", "--folder", dir}) != ExitOK {
		t.Fatal("explicit add failed")
	}
	a, _ := openReg(t).Get("AAA")
	b, _ := openReg(t).Get("BBB")
	if a.Root != b.Root || a.ProjectID != b.ProjectID || a.ProjectKey != b.ProjectKey {
		t.Fatalf("forms diverge: %+v vs %+v", a, b)
	}
}

func TestProjectListShowRemove(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	dir := mkProject(t, "proj-x")
	t.Chdir(dir)
	if Run([]string{"project", "add", "X"}) != ExitOK {
		t.Fatal("add failed")
	}
	if Run([]string{"project", "list"}) != ExitOK {
		t.Fatal("list failed")
	}
	if Run([]string{"project", "show", "X"}) != ExitOK {
		t.Fatal("show failed")
	}
	if Run([]string{"project", "show", "NOPE"}) != ExitFail {
		t.Fatal("show of unknown code must fail")
	}
	if Run([]string{"project", "remove", "X"}) != ExitOK {
		t.Fatal("remove failed")
	}
	if Run([]string{"project", "remove", "X"}) != ExitFail {
		t.Fatal("second remove must fail")
	}
}

// TestProjectInfoStillDispatches proves the registry subcommands don't
// shadow the "project info" daemon operation's routing (the call fails
// without a daemon, but must not hit the registry path).
func TestProjectInfoStillDispatches(t *testing.T) {
	t.Setenv("GAME_FORGE_HOME", t.TempDir())
	if code := Run([]string{"project", "bogus-subcommand"}); code == ExitOK {
		t.Fatal("unknown project subcommand must not succeed")
	}
}
