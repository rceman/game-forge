package cli

import (
	"fmt"
	"os"

	"github.com/rceman/game-forge/internal/project"
)

// cmdProject owns the durable project registry commands. Registration is a
// deliberate local admin action on machine-local state — it never goes
// through the daemon, so it works with the daemon stopped and a running
// daemon sees changes immediately (the registry is read on resolution).
func cmdRegistry(args []string) int {
	switch first(args) {
	case "add":
		return projectAdd(args[1:])
	case "list":
		return projectList()
	case "show":
		return projectShow(args[1:])
	case "remove":
		return projectRemove(args[1:])
	default:
		fmt.Fprintln(os.Stderr, `game-forge project: expected subcommand

  project add <code> [--replace]            register the current project
  project add --code X --folder P [--replace]  register an explicit project
  project list                              list registered projects
  project show <code>                       show one registration
  project remove <code>                     remove a registration`)
		return ExitUsage
	}
}

// projectAdd registers a project. Two forms converge on the same registry:
// the shorthand `project add CODE` discovers the project root upward from the
// cwd, and `--code/--folder` registers an explicit path. Mixing the two forms
// is ambiguous and rejected.
func projectAdd(args []string) int {
	var code, folder string
	var positional string
	var replace bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--code":
			if i+1 >= len(args) {
				return projectAddUsage()
			}
			code = args[i+1]
			i++
		case "--folder":
			if i+1 >= len(args) {
				return projectAddUsage()
			}
			folder = args[i+1]
			i++
		case "--replace":
			replace = true
		default:
			if positional != "" || len(args[i]) == 0 || args[i][0] == '-' {
				return projectAddUsage()
			}
			positional = args[i]
		}
	}
	// One form per invocation: positional is the current-project shorthand;
	// --code/--folder is the explicit pair.
	if positional != "" && (code != "" || folder != "") {
		fmt.Fprintln(os.Stderr, "game-forge project add: do not mix a positional code with --code/--folder")
		return ExitUsage
	}
	if code != "" && folder == "" || code == "" && folder != "" {
		fmt.Fprintln(os.Stderr, "game-forge project add: --code and --folder are used together")
		return ExitUsage
	}
	if positional == "" && code == "" {
		return projectAddUsage()
	}
	if positional != "" {
		code = positional
		var err error
		folder, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "game-forge project add: %v\n", err)
			return ExitFail
		}
	}

	reg, err := project.OpenRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge project add: %v\n", err)
		return ExitFail
	}
	rec, err := reg.Add(code, folder, replace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge project add: %v\n", err)
		return ExitFail
	}
	fmt.Printf("registered %s\n  root:    %s\n  project: %s\n  key:     %s\n",
		rec.Code, rec.Root, rec.ProjectID, rec.ProjectKey)
	return ExitOK
}

func projectAddUsage() int {
	fmt.Fprintln(os.Stderr, "usage: game-forge project add <code> [--replace]\n       game-forge project add --code <code> --folder <path> [--replace]")
	return ExitUsage
}

// projectList prints every registered project.
func projectList() int {
	reg, err := project.OpenRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge project list: %v\n", err)
		return ExitFail
	}
	list := reg.List()
	if len(list) == 0 {
		fmt.Println("no projects registered")
		return ExitOK
	}
	fmt.Printf("%-16s %-24s %s\n", "CODE", "PROJECT KEY", "ROOT")
	for _, p := range list {
		fmt.Printf("%-16s %-24s %s\n", p.Code, p.ProjectKey, p.Root)
	}
	return ExitOK
}

// projectShow prints one registration.
func projectShow(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: game-forge project show <code>")
		return ExitUsage
	}
	reg, err := project.OpenRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge project show: %v\n", err)
		return ExitFail
	}
	rec, ok := reg.Get(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "game-forge project show: unknown_project: %s\n", args[0])
		return ExitFail
	}
	fmt.Printf("%s\n  root:    %s\n  project: %s\n  key:     %s\n",
		rec.Code, rec.Root, rec.ProjectID, rec.ProjectKey)
	return ExitOK
}

// projectRemove deletes a registration.
func projectRemove(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: game-forge project remove <code>")
		return ExitUsage
	}
	reg, err := project.OpenRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge project remove: %v\n", err)
		return ExitFail
	}
	code, err := project.NormalizeCode(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "game-forge project remove: %v\n", err)
		return ExitUsage
	}
	if err := reg.Remove(code); err != nil {
		fmt.Fprintf(os.Stderr, "game-forge project remove: %v\n", err)
		return ExitFail
	}
	fmt.Printf("removed %s\n", code)
	return ExitOK
}
