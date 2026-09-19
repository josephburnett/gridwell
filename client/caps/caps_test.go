package caps

import (
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/client/errsurface"
)

func TestDerive(t *testing.T) {
	urlBridge := Bridge{LiveURL: true}
	if !Derive(urlBridge, false).LiveURL {
		t.Errorf("a bridge declaring live url must enable live URL views")
	}
	if Derive(NoBridge(), false).LiveURL {
		t.Errorf("no bridge must disable live URL views")
	}
	// A bridge that declares nothing is a plain browser.
	if Derive(Bridge{}, false).LiveURL {
		t.Errorf("a bridge declaring nothing must disable live URL views")
	}
	// Each half is declared on its own, so a host with one does not get both.
	if c := Derive(Bridge{LiveURL: true}, false); c.ChoiceMenu {
		t.Errorf("live url alone must not imply a native menu, got %+v", c)
	}
	if c := Derive(Bridge{ChoiceMenu: true}, false); !c.ChoiceMenu || c.LiveURL {
		t.Errorf("a menu-only bridge: want ChoiceMenu alone, got %+v", c)
	}
	if c := Derive(urlBridge, false); !c.Shells {
		t.Errorf("shells enabled + bridge: want Shells, got %+v", c)
	}
	if c := Derive(urlBridge, true); c.Shells {
		t.Errorf("shells_disabled must kill Shells, got %+v", c)
	}
	// The PTY rides the web door, so a browser attaches one too.
	if c := Derive(NoBridge(), false); !c.Shells {
		t.Errorf("browser host: shells are live there too, got %+v", c)
	}
	if c := Derive(NoBridge(), true); c.Shells {
		t.Errorf("browser host on a shells-disabled node: nothing, got %+v", c)
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
