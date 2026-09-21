package cli

import (
	"fmt"
	"strings"

	"github.com/josephburnett/gridwell/internal/config"
)

// RunStatus reports whether a `gridwell serve` holds this home's serve lock,
// see servelock.go, without starting anything. The holder's banner is
// re-emitted in the "already serving" shape, so the desktop app's
// --no-server mode discovers a separately-run server with one verb and never
// learns what a home or a lock is. Exit 0 means running, 1 means not.
func RunStatus(_ []string) int {
	home, err := config.Home()
	if err != nil {
		die("status", err)
		return 2
	}
	banner, running, err := probeServeLock(home)
	if err != nil {
		die("status", err)
		return 2
	}
	if running && strings.HasPrefix(banner, bannerPrefix) {
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
