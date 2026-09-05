package pluginhealth

import (
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
)

func TestClassify_Enterable(t *testing.T) {
	pl := rpc.PluginInfo{Label: "Home", RootGridID: "u/1"}
	if got := Classify(pl); got != Enterable {
		t.Errorf("Classify(%+v) = %v, want Enterable", pl, got)
	}
}

func TestClassify_Broken(t *testing.T) {
	pl := rpc.PluginInfo{Label: "Files", InfoError: "plugin not responding: dial tcp: connection refused"}
	if got := Classify(pl); got != Broken {
		t.Errorf("Classify(%+v) = %v, want Broken", pl, got)
	}
}

func TestClassify_Rootless(t *testing.T) {
	pl := rpc.PluginInfo{Label: "Files", RootGridID: "", InfoError: ""}
	if got := Classify(pl); got != Rootless {
		t.Errorf("Classify(%+v) = %v, want Rootless", pl, got)
	}
}

// TestClassify_BrokenTakesPrecedenceOverEmptyRoot: a plugin can only be
// Broken when Info truly failed (InfoError set); RootGridID empty on its own
// (with InfoError empty) must always mean Rootless, never Broken.
func TestClassify_BrokenAndRootlessAreDistinctForEmptyRoot(t *testing.T) {
	broken := rpc.PluginInfo{Label: "X", RootGridID: "", InfoError: "boom"}
	rootless := rpc.PluginInfo{Label: "X", RootGridID: "", InfoError: ""}
	if Classify(broken) == Classify(rootless) {
		t.Fatal("broken and rootless must classify differently even though both have empty RootGridID")
	}
	if Classify(broken) != Broken {
		t.Errorf("Classify(broken) = %v, want Broken", Classify(broken))
	}
	if Classify(rootless) != Rootless {
		t.Errorf("Classify(rootless) = %v, want Rootless", Classify(rootless))
	}
}

func TestClickNotice_Enterable_NotOk(t *testing.T) {
	pl := rpc.PluginInfo{Label: "Home", RootGridID: "u/1"}
	_, _, _, ok := ClickNotice(pl)
	if ok {
		t.Error("ClickNotice for an enterable plugin must return ok=false (caller should descend)")
	}
}

func TestClickNotice_Broken(t *testing.T) {
	pl := rpc.PluginInfo{UUID: "uux1", Label: "Files", InfoError: "plugin not responding: boom"}
	sev, source, msg, ok := ClickNotice(pl)
	if !ok {
		t.Fatal("ClickNotice for a broken plugin must return ok=true")
	}
	if sev != errsurface.Error {
		t.Errorf("severity = %v, want errsurface.Error", sev)
	}
	if source != "launcher:uux1" {
		t.Errorf("source = %q, want launcher:uux1 (the UUID — labels can collide)", source)
	}
	if !strings.Contains(msg, "Files") || !strings.Contains(msg, "plugin not responding: boom") {
		t.Errorf("message = %q, want it to name the plugin and carry InfoError", msg)
	}
}

func TestClickNotice_Rootless(t *testing.T) {
	pl := rpc.PluginInfo{UUID: "uux1", Label: "Files"}
	sev, source, msg, ok := ClickNotice(pl)
	if !ok {
		t.Fatal("ClickNotice for a rootless plugin must return ok=true")
	}
	if sev != errsurface.Info {
		t.Errorf("severity = %v, want errsurface.Info", sev)
	}
	if source != "launcher:uux1" {
		t.Errorf("source = %q, want launcher:uux1 (the UUID — labels can collide)", source)
	}
	if !strings.Contains(msg, "no root configured") {
		t.Errorf("message = %q, want it to mention the missing root config", msg)
	}
}

// TestClickNotice_SourceKeyedByLabelCoalesces documents the coalescing
// contract this feeds into errsurface.Surface.Report: repeated clicks on the
// same plugin produce the same source key, so they update one row rather
// than scrolling the strip.
func TestClickNotice_SourceKeyedByLabelCoalesces(t *testing.T) {
	pl := rpc.PluginInfo{Label: "Files", InfoError: "boom"}
	_, s1, _, _ := ClickNotice(pl)
	_, s2, _, _ := ClickNotice(pl)
	if s1 != s2 {
		t.Errorf("source key changed across identical clicks: %q vs %q", s1, s2)
	}
}

// A connection row that hasn't learned its root is waiting, not
// misconfigured — the notice must say so, never point at config.root, which
// doesn't exist for connections. The row is recognized by its declared kind,
// so an unsegmented uuid reads the same as a chained one: this case is built
// through rpc.ConnectionRow, the one minter.
func TestClickNotice_PendingConnection(t *testing.T) {
	pl := rpc.ConnectionRow(rpc.ConnectionInfo{UUID: "conn1", Label: "rtb"})
	if _, _, msg, _ := ClickNotice(pl); !strings.Contains(msg, "hasn't answered") {
		t.Fatalf("an unsegmented connection uuid must still read as waiting: %q", msg)
	}
	pl = rpc.ConnectionRow(rpc.ConnectionInfo{UUID: "sshx/conn1", Label: "rtb"})
	sev, source, msg, ok := ClickNotice(pl)
	if !ok || source != "launcher:sshx/conn1" {
		t.Fatalf("notice = %v %q %q %v (keyed by UUID — labels can collide)", sev, source, msg, ok)
	}
	if !strings.Contains(msg, "hasn't answered") {
		t.Fatalf("pending-connection wording: %q", msg)
	}
	// With a recorded dial failure the detail rides InfoError → Broken.
	pl.InfoError = "dial tcp 127.0.0.1:1: connection refused"
	_, _, msg, ok = ClickNotice(pl)
	if !ok || !strings.Contains(msg, "connection refused") {
		t.Fatalf("dial detail must surface: %q ok=%v", msg, ok)
	}
}

// TestUnrootedLink pins the three facts the tint reads together. A well link
// that resolved a child grid is enterable; a leaf link is not a well, so it
// is never tinted however it points; and a plain local well is not a
// reference at all.
func TestUnrootedLink(t *testing.T) {
	cases := []struct {
		name string
		tile rpc.Tile
		want bool
	}{
		{"launcher with no root", rpc.Tile{Kind: rpc.KindWell, Reference: true}, true},
		{"launcher with a root", rpc.Tile{Kind: rpc.KindWell, Reference: true, ChildGridID: "fs/1"}, false},
		{"leaf link", rpc.Tile{Kind: rpc.KindText, Reference: true, LinkTargetID: "fs/2"}, false},
		{"plain well", rpc.Tile{Kind: rpc.KindWell, ChildGridID: "3"}, false},
	}
	for _, c := range cases {
		if got := UnrootedLink(&c.tile); got != c.want {
			t.Errorf("%s: UnrootedLink = %v, want %v", c.name, got, c.want)
		}
	}
}
