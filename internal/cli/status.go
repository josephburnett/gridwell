package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/josephburnett/gridwell/internal/config"
)

// RunStatus reports whether a `gridwell serve` currently holds this home's
// serve lock (servelock.go), without starting anything. The running
// holder's banner is re-emitted in the same "already serving" shape a
// conflicting serve prints, so the desktop app's --no-server mode uses
// this one verb to DISCOVER a separately-run server (address + auth token)
// instead of ever learning what a home or a lock is.
//
// Exit 0 = a server is running (banner printed); exit 1 = none is.
func RunStatus(_ []string) int {
	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: %v\n", err)
		return 2
	}
	banner, running, err := probeServeLock(home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: %v\n", err)
		return 2
	}
	if running && strings.HasPrefix(banner, "gridwell: serving on ") {
		fmt.Println("gridwell: already " + strings.TrimPrefix(banner, "gridwell: "))
		return 0
	}
	if running {
		// Held but no banner yet: a serve is mid-start.
		fmt.Println("gridwell: a serve is starting up")
		return 0
	}
	fmt.Println("gridwell: not serving")
	return 1
}
