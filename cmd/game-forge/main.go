// Command game-forge is the command-line frontend for the Game Forge
// development and validation harness.
package main

import (
	"os"

	"github.com/rceman/game-forge/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
