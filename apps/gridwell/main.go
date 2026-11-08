// gridwell is the node's binary, the web client embedded in it.
package main

import (
	"os"

	"github.com/josephburnett/gridwell/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:])) }
