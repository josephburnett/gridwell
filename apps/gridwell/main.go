// gridwell is the host binary. Every plugin is an out-of-process
// gridwell-plugin-* binary, so this module imports no plugin implementation;
// test/boundary pins that.
package main

import (
	"os"

	"github.com/josephburnett/gridwell/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:])) }
