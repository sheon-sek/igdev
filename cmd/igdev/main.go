// Command igdev is the Ignition local-development toolchain CLI.
package main

import (
	"os"

	"github.com/sheon-sek/igdev/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args, cli.New(os.Stdout, os.Stderr, os.Environ())))
}
