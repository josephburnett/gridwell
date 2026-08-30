// gridwell is the stock host binary, a leaf that bundles nothing: every plugin
// is an out-of-process gridwell-plugin-* binary named by config or found on
// PATH. This module imports no plugin implementations, which test/boundary
// pins. The mobile bind is the one leaf that bundles.
package main

import (
	"os"

	"github.com/josephburnett/gridwell/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:], nil)) }
