package plugin

// The crash-to-notice latency, against a real subprocess. respawn_e2e_test.go
// proves a crash reaches the user; watchInterval is how long that takes, so
// the wait here is derived from it and from nothing else.

import (
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/internal/plugintest"
)

const watchUUID = "pwatch1"

// A process that exits is reported down within a few watch ticks. The watch
// polls because go-plugin offers no exit signal, so this is the whole of the
// delay between a plugin dying and the notice row saying so.
func TestACrashIsNoticedWithinTheWatchInterval(t *testing.T) {
	// The plugin inherits this process's environment, so the test redirects
	// its own home. Nothing may write into the developer's.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	sup, err := Supervise(watchUUID, "fs", plugintest.Binary(t, "fs"), map[string]string{
		"root": t.TempDir(), "uuid": watchUUID, "kind": "fs", "state_dir": t.TempDir(),
	})
	if err != nil {
		t.Fatalf("supervise: %v", err)
	}
	t.Cleanup(sup.Close)

	down := make(chan time.Time, 4)
	cancel := sup.OnHealth(func(healthy bool, _ string) {
		if !healthy {
			select {
			case down <- time.Now():
			default:
			}
		}
	})
	t.Cleanup(cancel)

	if healthy, detail := sup.Health(); !healthy {
		t.Fatalf("the plugin is down before anything killed it: %s", detail)
	}
	// The pid comes off the supervisor's own process handle: the test kills
	// out of band, because the supervisor's own kill is a stop, not a crash.
	sup.mu.Lock()
	pid, perr := strconv.Atoi(sup.proc.ID())
	sup.mu.Unlock()
	if perr != nil {
		t.Fatalf("plugin pid: %v", perr)
	}

	killedAt := time.Now()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %d: %v", pid, err)
	}
	select {
	case at := <-down:
		// One interval is the poll and the rest is slack for the exit
		// becoming observable, so anything else on the path — another poll
		// layer, the respawn pause — shows up here.
		if took := at.Sub(killedAt); took > 3*watchInterval {
			t.Fatalf("the crash was noticed after %v; watchInterval is %v and it is the whole of the latency", took, watchInterval)
		}
	case <-time.After(20 * watchInterval):
		t.Fatalf("a dead subprocess was never reported down; watchInterval is %v", watchInterval)
	}
}
