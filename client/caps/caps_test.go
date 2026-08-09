package caps

import (
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/client/errsurface"
)

func TestDerive(t *testing.T) {
	if !Derive(true, false).LiveURL {
		t.Errorf("bridge present must enable live URL views")
	}
	if Derive(false, false).LiveURL {
		t.Errorf("no bridge must disable live URL views")
	}
	if c := Derive(true, false); !c.Shells || !c.LiveShell {
		t.Errorf("shells enabled + bridge: want Shells and LiveShell, got %+v", c)
	}
	if c := Derive(true, true); c.Shells || c.LiveShell {
		t.Errorf("shells_disabled must kill both Shells and LiveShell, got %+v", c)
	}
	if c := Derive(false, false); c.LiveShell || !c.Shells {
		t.Errorf("browser host: shells exist (frozen) but never live, got %+v", c)
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
