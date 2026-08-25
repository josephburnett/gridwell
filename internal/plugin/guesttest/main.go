// The TEST-ONLY gridwell.v1 guest binary: the native store served over
// the real go-plugin transport — exactly the shape a third-party
// gridwell.v1 plugin ships (docs/plugin-authoring.md). The subprocess
// e2e tests (internal/plugin) build and spawn it, so the guest door
// (guest.Serve, the env handshake, the exit-on-identity-mismatch
// contract) stays exercised now that no shipped binary uses it. The
// *test-suffixed dir exempts it from the deadcode gate; never shipped.
package main

import (
	"fmt"
	"os"

	"github.com/josephburnett/gridwell/api/guest"
	"github.com/josephburnett/gridwell/internal/local"
)

func main() {
	cfg := guest.Config()
	st, err := local.OpenVerified(cfg["db_file"], cfg["uuid"], cfg["kind"])
	if err != nil {
		fmt.Fprintln(os.Stderr, "guesttest:", err)
		os.Exit(1)
	}
	guest.Serve(local.New(st, nil))
}
