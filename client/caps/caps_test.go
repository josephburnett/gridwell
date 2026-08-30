package caps

import (
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/client/errsurface"
)

func TestDerive(t *testing.T) {
	if !Derive(LegacyBridge(), false).LiveURL {
		t.Errorf("bridge present must enable live URL views")
	}
	if Derive(NoBridge(), false).LiveURL {
		t.Errorf("no bridge must disable live URL views")
	}
	if c := Derive(LegacyBridge(), false); !c.Shells || !c.LiveShell {
		t.Errorf("shells enabled + bridge: want Shells and LiveShell, got %+v", c)
	}
	if c := Derive(LegacyBridge(), true); c.Shells || c.LiveShell {
		t.Errorf("shells_disabled must kill both Shells and LiveShell, got %+v", c)
	}
	// The whole point of the 2026-08-29 transport move: a plain browser
	// (a phone pointed at the node) attaches a live PTY exactly like the
	// desktop, because the PTY rides the web door. Only the NODE can say
	// no.
	if c := Derive(NoBridge(), false); !c.LiveShell || !c.Shells {
		t.Errorf("browser host: shells are live there too, got %+v", c)
	}
	if c := Derive(NoBridge(), true); c.LiveShell || c.Shells {
		t.Errorf("browser host on a shells-disabled node: nothing, got %+v", c)
	}
	// The mobile shape (2026-08-13): a bridge that declares live url views
	// only. It says nothing about shells any more — the host has no shell
	// half to implement.
	mobile := Bridge{Present: true, LiveURL: true}
	if c := Derive(mobile, false); !c.LiveURL || !c.LiveShell || !c.Shells {
		t.Errorf("url-only bridge: shells still ride the web door, got %+v", c)
	}
}

func TestGoLiveNotice(t *testing.T) {
	sev, source, message := GoLiveNotice()
	if sev != errsurface.Info {
		t.Errorf("severity = %v, want Info — a missing capability is expected, not a failure", sev)
	}
	if source == "" {
		t.Fatalf("source must be a stable key so repeated taps coalesce")
	}
	if errsurface.Sticky(source) {
		t.Errorf("notice for source %q must expire like any one-shot, not persist", source)
	}
	if !strings.Contains(message, "desktop") {
		t.Errorf("message %q should point the user at the desktop app", message)
	}
}
