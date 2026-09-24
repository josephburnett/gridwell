package plugin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/internal/trace"
)

// A plugin's stderr does not pass through the node's log, so log.SetOutput
// cannot see it: the subprocess writes to the writer compose.LoadPlugin is
// given. The seam is the whole spawn — Supervise builds that writer, compose
// hands it to go-plugin, and the subprocess's line comes out the other end.
func TestAPluginSubprocessStderrReachesTheRing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the noisy stand-in is a shell script")
	}
	bin := filepath.Join(t.TempDir(), "noisy")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'noisy-plugin-said-this' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Not a plugin: it never handshakes, so the spawn fails after its stderr
	// has been read.
	if _, err := Supervise("noisyuuid", "fs", bin, nil); err == nil {
		t.Fatal("a binary that does not speak the handshake was accepted")
	}
	for _, rec := range trace.Default().Snapshot() {
		if !strings.Contains(rec.Msg, "noisy-plugin-said-this") {
			continue
		}
		if rec.Origin != tracewire.OriginPlugin || rec.Src != "noisyuuid" {
			t.Fatalf("the plugin's line landed as %+v, want the plugin origin under its own id", rec)
		}
		return
	}
	t.Fatal("a plugin subprocess's stderr reached no record")
}
