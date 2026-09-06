package pluginhealth

import (
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
)

// The whole classification, as a table. Three statuses: enterable, waiting
// (asked, no answer yet — only a connection row can be in it), and broken,
// which every failure collapses into.
func TestClassifyTable(t *testing.T) {
	cases := []struct {
		name string
		pl   rpc.PluginInfo
		want Status
	}{
		{"rooted plugin", rpc.PluginInfo{Label: "Home", RootGridID: "u/1"}, Enterable},
		{"rooted connection", rpc.ConnectionRow(rpc.ConnectionInfo{UUID: "c1", RootGridID: "c1/1"}), Enterable},
		{"info failed", rpc.PluginInfo{Label: "Files", InfoError: "plugin not responding: connection refused"}, Broken},
		{"answered, declared no root", rpc.PluginInfo{Label: "Files"}, Broken},
		{"connection not answered yet", rpc.ConnectionRow(rpc.ConnectionInfo{UUID: "c1", Label: "rtb"}), Waiting},
		{"connection that failed to dial", rpc.ConnectionRow(rpc.ConnectionInfo{UUID: "c1", Label: "rtb",
			StatusDetail: "dial tcp 127.0.0.1:1: connection refused"}), Broken},
		// A rooted row with a recorded error is broken: the error is the
		// newer fact, and descending into a namespace that just failed would
		// hang rather than say so.
		{"rooted but errored", rpc.PluginInfo{Label: "Files", RootGridID: "u/1", InfoError: "boom"}, Broken},
	}
	for _, c := range cases {
		if got := Classify(c.pl); got != c.want {
			t.Errorf("%s: Classify = %v, want %v", c.name, got, c.want)
		}
	}
}

// The two failures present identically — one status, one tint, one severity —
// and differ only in the reason text the click report carries for debugging.
func TestBrokenIsOneStatusWithTheReasonInTheText(t *testing.T) {
	failed := rpc.PluginInfo{UUID: "u1", Label: "Files", InfoError: "plugin not responding: boom"}
	noRoot := rpc.PluginInfo{UUID: "u2", Label: "Files"}
	if Classify(failed) != Broken || Classify(noRoot) != Broken {
		t.Fatalf("both failures must be Broken: %v %v", Classify(failed), Classify(noRoot))
	}
	sev1, _, msg1, _ := ClickNotice(failed)
	sev2, _, msg2, _ := ClickNotice(noRoot)
	if sev1 != errsurface.Error || sev2 != errsurface.Error {
		t.Errorf("severities = %v %v, want both errsurface.Error", sev1, sev2)
	}
	if !strings.Contains(msg1, "plugin not responding: boom") {
		t.Errorf("message = %q, want the recorded failure as the reason", msg1)
	}
	if !strings.Contains(msg2, "no root configured") {
		t.Errorf("message = %q, want the missing-root reason", msg2)
	}
	if !strings.HasPrefix(msg1, "Files: ") || !strings.HasPrefix(msg2, "Files: ") {
		t.Errorf("messages = %q / %q, want one shape: the label, then the reason", msg1, msg2)
	}
}

func TestClickNotice_Enterable_NotOk(t *testing.T) {
	pl := rpc.PluginInfo{Label: "Home", RootGridID: "u/1"}
	_, _, _, ok := ClickNotice(pl)
	if ok {
		t.Error("ClickNotice for an enterable plugin must return ok=false (caller should descend)")
	}
}

func TestClickNotice_KeyedByUUID(t *testing.T) {
	pl := rpc.PluginInfo{UUID: "uux1", Label: "Files", InfoError: "plugin not responding: boom"}
	_, source, _, ok := ClickNotice(pl)
	if !ok {
		t.Fatal("ClickNotice for a broken plugin must return ok=true")
	}
	if source != "launcher:uux1" {
		t.Errorf("source = %q, want launcher:uux1 (the UUID — labels can collide)", source)
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

// A connection row that hasn't learned its root is waiting, not broken — the
// notice reads like the loading it is, at Info severity, and never points at
// config.root, which doesn't exist for connections. The row is recognized by
// its declared kind, so an unsegmented uuid reads the same as a chained one:
// this case is built through rpc.ConnectionRow, the one minter.
func TestClickNotice_PendingConnection(t *testing.T) {
	pl := rpc.ConnectionRow(rpc.ConnectionInfo{UUID: "conn1", Label: "rtb"})
	if _, _, msg, _ := ClickNotice(pl); !strings.Contains(msg, "loading rtb") {
		t.Fatalf("an unsegmented connection uuid must still read as loading: %q", msg)
	}
	pl = rpc.ConnectionRow(rpc.ConnectionInfo{UUID: "sshx/conn1", Label: "rtb"})
	sev, source, msg, ok := ClickNotice(pl)
	if !ok || source != "launcher:sshx/conn1" {
		t.Fatalf("notice = %v %q %q %v (keyed by UUID — labels can collide)", sev, source, msg, ok)
	}
	if sev != errsurface.Info {
		t.Errorf("severity = %v, want errsurface.Info: waiting is not a failure", sev)
	}
	if strings.Contains(msg, "config.root") {
		t.Errorf("pending-connection wording must not point at config.root: %q", msg)
	}
	// With a recorded dial failure the detail rides InfoError → Broken.
	pl.InfoError = "dial tcp 127.0.0.1:1: connection refused"
	sev, _, msg, ok = ClickNotice(pl)
	if !ok || sev != errsurface.Error || !strings.Contains(msg, "connection refused") {
		t.Fatalf("dial detail must surface as an error: %v %q ok=%v", sev, msg, ok)
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
