package cli

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/josephburnett/gridwell/internal/urldriver"
)

// openBrowserFlags is the parsed flag set for `gridwell open-browser`.
// Same browser / binary / profile-dir trio as `serve` so the same
// invocation idioms work — but no DB or HTTP options because this
// command does nothing but launch the chosen browser headful against
// the gridwell-managed profile.
type openBrowserFlags struct {
	BrowserName string
	BrowserBin  string
	ProfileDir  string
}

// parseOpenBrowserFlags parses the `open-browser` flag set. Mirrors
// parseServeFlags so the two share the same flag UX.
func parseOpenBrowserFlags(args []string) (openBrowserFlags, error) {
	fs := flag.NewFlagSet("open-browser", flag.ContinueOnError)
	var f openBrowserFlags
	fs.StringVar(&f.BrowserName, "browser", "chromium", "browser brand: "+strings.Join(sortedBrands(), ", "))
	fs.StringVar(&f.BrowserBin, "browser-bin", "", "explicit browser binary path (overrides --browser lookup)")
	fs.StringVar(&f.ProfileDir, "profile-dir", "", "explicit user-data-dir (overrides ~/.gridwell/profiles/<browser>)")
	args = reorderFlagsFirst(args, func(name string) bool {
		switch name {
		case "browser", "browser-bin", "profile-dir":
			return true
		}
		return false
	})
	if err := fs.Parse(args); err != nil {
		return openBrowserFlags{}, err
	}
	return f, nil
}

// RunOpenBrowser launches the chosen browser headful against the
// gridwell-managed profile directory, then waits for it to exit. Used
// for one-time sign-in setup: launch, log into the sites you care about,
// quit, then `gridwell serve` reuses the cookies headless.
//
// The browser runs in its own process group (Setpgid). On SIGINT /
// SIGTERM to this process, we forward SIGTERM to that group so every
// helper / renderer / GPU process Chrome spawned goes down with the
// parent — no orphaned background processes. If the browser hasn't
// exited after a short grace period we escalate to SIGKILL.
func RunOpenBrowser(args []string) int {
	f, err := parseOpenBrowserFlags(args)
	if err != nil {
		return 2
	}

	binPath, err := urldriver.ResolveBinary(f.BrowserName, f.BrowserBin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open-browser: %v\n", err)
		return 1
	}
	profileDir := f.ProfileDir
	if profileDir == "" {
		profileDir, err = urldriver.DefaultProfileDir(f.BrowserName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open-browser: %v\n", err)
			return 1
		}
	}
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "open-browser: profile dir: %v\n", err)
		return 1
	}

	cmdArgs := []string{"--user-data-dir=" + profileDir}
	for _, flag := range urldriver.BrandExtraFlags(f.BrowserName) {
		cmdArgs = append(cmdArgs, "--"+flag)
	}
	cmd := exec.Command(binPath, cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// New process group so we can kill the browser and every helper it
	// spawned with a single `kill -SIGTERM -pgid`.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	fmt.Printf("gridwell: launching %s\n", f.BrowserName)
	fmt.Printf("gridwell:   binary  = %s\n", binPath)
	fmt.Printf("gridwell:   profile = %s\n", profileDir)
	fmt.Printf("gridwell: ctrl-c to quit (sends SIGTERM to the browser)\n")

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "open-browser: start: %v\n", err)
		return 1
	}

	// Forward SIGINT / SIGTERM to the browser group. Wait runs in its
	// own goroutine so we can race it against the signal channel; the
	// loser is interrupted with SIGKILL.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case sig := <-stop:
		fmt.Printf("gridwell: received %s, terminating %s\n", sig, f.BrowserName)
		terminateGroup(cmd, syscall.SIGTERM)
		select {
		case err := <-done:
			return browserExitCode(err)
		case <-time.After(5 * time.Second):
			fmt.Fprintln(os.Stderr, "gridwell: browser did not exit after 5s, sending SIGKILL")
			terminateGroup(cmd, syscall.SIGKILL)
			<-done
			return 1
		}
	case err := <-done:
		return browserExitCode(err)
	}
}

// terminateGroup signals the browser's whole process group. Falls back
// to signaling just the leader if Getpgid fails (which would be unusual
// — we set Setpgid:true).
func terminateGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, sig)
		return
	}
	_ = cmd.Process.Signal(sig)
}

// browserExitCode unpacks an exec.Cmd.Wait error into a process exit
// code. A normal browser quit returns 0; a signal-induced exit (our
// SIGTERM, for example) is treated as success because the user asked
// for it.
func browserExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			if status.Signaled() {
				return 0
			}
			return status.ExitStatus()
		}
	}
	fmt.Fprintf(os.Stderr, "open-browser: %v\n", err)
	return 1
}
