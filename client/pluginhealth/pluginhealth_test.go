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

// TestClassify_InstanceGridReadsRootless: the Parameterized status
// retired with the instance picker (2026-08-23) — a parameterized
// plugin's bare row (listed only when its instance grid is unreadable)
// is just rootless-inert now; its instances present as rows of their own.
func TestClassify_InstanceGridReadsRootless(t *testing.T) {
	pl := rpc.PluginInfo{Label: "connections", InstanceGridID: "s/0"}
	if got := Classify(pl); got != Rootless {
		t.Errorf("Classify(%+v) = %v, want Rootless", pl, got)
	}
}

// TestClassify_RootWinsOverInstanceGrid: a plugin declaring BOTH grids is
// rooted — the root grid is where a click lands; the instance grid is
// storage. (No current plugin does this, but the precedence must be pinned
// so adding an instance grid can never un-root a plugin.)
func TestClassify_RootWinsOverInstanceGrid(t *testing.T) {
	pl := rpc.PluginInfo{Label: "X", RootGridID: "u/1", InstanceGridID: "u/9"}
	if got := Classify(pl); got != Enterable {
		t.Errorf("Classify(%+v) = %v, want Enterable", pl, got)
	}
}

// TestClassify_BrokenWinsOverInstanceGrid: InfoError set means Broken even
// if a (cached/stale) instance grid id is present.
func TestClassify_BrokenWinsOverInstanceGrid(t *testing.T) {
	pl := rpc.PluginInfo{Label: "X", InstanceGridID: "s/0", InfoError: "boom"}
	if got := Classify(pl); got != Broken {
		t.Errorf("Classify(%+v) = %v, want Broken", pl, got)
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
	pl := rpc.PluginInfo{Label: "Files", InfoError: "plugin not responding: boom"}
	sev, source, msg, ok := ClickNotice(pl)
	if !ok {
		t.Fatal("ClickNotice for a broken plugin must return ok=true")
	}
	if sev != errsurface.Error {
		t.Errorf("severity = %v, want errsurface.Error", sev)
	}
	if source != "launcher:Files" {
		t.Errorf("source = %q, want launcher:Files", source)
	}
	if !strings.Contains(msg, "Files") || !strings.Contains(msg, "plugin not responding: boom") {
		t.Errorf("message = %q, want it to name the plugin and carry InfoError", msg)
	}
}

func TestClickNotice_Rootless(t *testing.T) {
	pl := rpc.PluginInfo{Label: "Files"}
	sev, source, msg, ok := ClickNotice(pl)
	if !ok {
		t.Fatal("ClickNotice for a rootless plugin must return ok=true")
	}
	if sev != errsurface.Info {
		t.Errorf("severity = %v, want errsurface.Info", sev)
	}
	if source != "launcher:Files" {
		t.Errorf("source = %q, want launcher:Files", source)
	}
	if !strings.Contains(msg, "no root configured") {
		t.Errorf("message = %q, want it to mention the missing root config", msg)
	}
}

// TestClickNotice_SourceKeyedByLabelCoalesces documents the coalescing
// contract this feeds into errsurface.Surface.Report: repeated clicks on the
// same plugin must produce the SAME source key so they update one row rather
// than scrolling the strip.
func TestClickNotice_SourceKeyedByLabelCoalesces(t *testing.T) {
	pl := rpc.PluginInfo{Label: "Files", InfoError: "boom"}
	_, s1, _, _ := ClickNotice(pl)
	_, s2, _, _ := ClickNotice(pl)
	if s1 != s2 {
		t.Errorf("source key changed across identical clicks: %q vs %q", s1, s2)
	}
}
