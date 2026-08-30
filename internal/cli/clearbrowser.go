package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// RunClearBrowserData implements `gridwell clear-browser-data`: it deletes
// the desktop app's persist:gridwell partition storage, the one host-local
// Chromium session every live url tile browses on, including cookies, local
// storage, and caches. Clearing browser state is an operator action on the
// profile, not an in-page gesture. It refuses while the app is running,
// because Chromium owns the profile then — it holds SingletonLock in it —
// and would race the delete.
func RunClearBrowserData(args []string) int {
	fs := flag.NewFlagSet("clear-browser-data", flag.ExitOnError)
	userData := fs.String("user-data", "",
		"Electron profile directory (default: <os config dir>/gridwell-desktop)")
	_ = fs.Parse(args)
	dir := *userData
	if dir == "" {
		cfg, err := os.UserConfigDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot resolve the config dir: %v (pass --user-data)\n", err)
			return 1
		}
		dir = filepath.Join(cfg, "gridwell-desktop")
	}
	return clearBrowserData(os.Stderr, dir)
}

// clearBrowserData is the testable body: refuse on a live profile, no-op
// cleanly when there is nothing to clear, otherwise remove the partition.
func clearBrowserData(w io.Writer, userData string) int {
	if _, err := os.Lstat(filepath.Join(userData, "SingletonLock")); err == nil {
		fmt.Fprintln(w, "refusing: the Gridwell desktop app appears to be running (SingletonLock present) — close it first")
		return 1
	}
	part := filepath.Join(userData, "Partitions", "gridwell")
	if _, err := os.Stat(part); os.IsNotExist(err) {
		fmt.Fprintf(w, "nothing to clear: %s does not exist\n", part)
		return 0
	}
	if err := os.RemoveAll(part); err != nil {
		fmt.Fprintf(w, "clear failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(w, "cleared browser session data: %s\n", part)
	return 0
}
