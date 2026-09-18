package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

// flagSet is a thin wrapper over the standard flag package that gives every
// command consistent help handling and exit codes.
type flagSet struct {
	name string
	fs   *flag.FlagSet
}

func newFlagSet(name string) *flagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return &flagSet{name: name, fs: fs}
}

func (f *flagSet) Bool(name string, def bool, usage string) *bool {
	return f.fs.Bool(name, def, usage)
}

func (f *flagSet) Duration(name string, def time.Duration, usage string) *time.Duration {
	return f.fs.Duration(name, def, usage)
}

func (f *flagSet) String(name, def, usage string) *string {
	return f.fs.String(name, def, usage)
}

// parse parses args. It returns (exitCode, true) when the caller should stop
// and exit with that code (help or a usage error).
func (f *flagSet) parse(args []string) (int, bool) {
	if err := f.fs.Parse(args); err != nil {
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

func (f *flagSet) printUsage(w io.Writer) {
	fmt.Fprintf(w, "Usage: game-forge %s [options]\n", f.name)
	f.fs.SetOutput(w)
	f.fs.PrintDefaults()
	f.fs.SetOutput(io.Discard)
}
