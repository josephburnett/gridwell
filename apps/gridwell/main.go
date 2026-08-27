// gridwell — the STOCK HOST binary (docs/plugin.md, a leaf composer that
// bundles nothing): every provider is an out-of-process gridwell-provider-*
// binary named by config or found on the PATH. This module imports ZERO
// provider implementations — the boundary lint pins it. Compare
// apps/gridwell-all, the bundled example.
package main

import (
	"os"

	"github.com/josephburnett/gridwell/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:], nil)) }
