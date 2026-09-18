package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// flagSet is a thin wrapper over the standard flag package that gives every
// command consistent help handling and exit codes.
type flagSet struct {
	name  string
	fs    *flag.FlagSet
	bools map[string]bool
}

func newFlagSet(name string) *flagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return &flagSet{name: name, fs: fs, bools: map[string]bool{}}
}

func (f *flagSet) Bool(name string, def bool, usage string) *bool {
	f.bools[name] = true
	return f.fs.Bool(name, def, usage)
}

func (f *flagSet) Duration(name string, def time.Duration, usage string) *time.Duration {
	return f.fs.Duration(name, def, usage)
}

func (f *flagSet) String(name, def, usage string) *string {
	return f.fs.String(name, def, usage)
}

func (f *flagSet) Int(name string, def int, usage string) *int {
	return f.fs.Int(name, def, usage)
}

// Var registers a custom flag value, e.g. a repeatable flag.
func (f *flagSet) Var(value flag.Value, name, usage string) {
	f.fs.Var(value, name, usage)
}

// Args returns the positional arguments left after parsing.
func (f *flagSet) Args() []string { return f.fs.Args() }

// StringList is a repeatable string flag ("--param a=b --param c=d").
type StringList []string

// String implements flag.Value.
func (s *StringList) String() string { return strings.Join(*s, ",") }

// Set implements flag.Value.
func (s *StringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// parse parses args. It returns (exitCode, true) when the caller should stop
// and exit with that code (help or a usage error).
//
// Flags are accepted before or after positional arguments: the standard flag
// package stops at the first positional, so args are reordered first.
func (f *flagSet) parse(args []string) (int, bool) {
	if err := f.fs.Parse(f.reorder(args)); err != nil {
		if err == flag.ErrHelp {
			f.printUsage(os.Stdout)
			return ExitOK, true
		}
		fmt.Fprintf(os.Stderr, "game-forge %s: %v\n", f.name, err)
		f.printUsage(os.Stderr)
		return ExitUsage, true
	}
	return 0, false
}

// reorder moves flags ahead of positionals, preserving order within each group.
func (f *flagSet) reorder(args []string) []string {
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			positionals = append(positionals, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(a, "-") && a != "-":
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if _, _, hasValue := strings.Cut(name, "="); hasValue {
				continue
			}
			if !f.bools[name] && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
		default:
			positionals = append(positionals, a)
		}
	}
	return append(flags, positionals...)
}

func (f *flagSet) printUsage(w io.Writer) {
	fmt.Fprintf(w, "Usage: game-forge %s [options]\n", f.name)
	f.fs.SetOutput(w)
	f.fs.PrintDefaults()
	f.fs.SetOutput(io.Discard)
}
