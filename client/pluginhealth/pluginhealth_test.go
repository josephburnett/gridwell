package pluginhealth

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
)

func TestClassifyTable(t *testing.T) {
	cases := []struct {
		name string
		pl   *gridwellv1.PluginInfo
		want Status
	}{
		{"rooted plugin", &gridwellv1.PluginInfo{Label: "Home", RootGridId: "u/1"}, Enterable},
		{"rooted connection", rpc.ConnectionRow("c1", "", "c1/1", "", rpc.Framing{}), Enterable},
		{"info failed", &gridwellv1.PluginInfo{Label: "Files", InfoError: "plugin not responding: connection refused"}, Broken},
		// A plugin contributes doorways rather than being one, so no root of
		// its own is the healthy shape.
		{"answered, entries and no root", &gridwellv1.PluginInfo{Label: "Mail",
			MenuEntries: []*gridwellv1.MenuEntry{{Id: "feed", Label: "Feed", GridId: "u/2"}}}, NoDoor},
		{"answered, nothing declared", &gridwellv1.PluginInfo{Label: "Files"}, NoDoor},
		{"connection not answered yet", rpc.ConnectionRow("c1", "rtb", "", "", rpc.Framing{}), Waiting},
		{"connection that failed to dial", rpc.ConnectionRow("c1", "rtb", "",
			"dial tcp 127.0.0.1:1: connection refused", rpc.Framing{}), Broken},
		// A recorded error outranks a root: the error is the newer fact.
		{"rooted but errored", &gridwellv1.PluginInfo{Label: "Files", RootGridId: "u/1", InfoError: "boom"}, Broken},
	}
	for _, c := range cases {
		if got := Classify(c.pl); got != c.want {
			t.Errorf("%s: Classify = %v, want %v", c.name, got, c.want)
		}
	}
}

// Every kind of failure collapses into Broken; the recorded text is what
// distinguishes them.
func TestBrokenIsOneStatusWithTheReasonInTheText(t *testing.T) {
	failed := &gridwellv1.PluginInfo{Uuid: "u1", Label: "Files", InfoError: "plugin not responding: boom"}
	dialed := rpc.ConnectionRow("u2", "Files", "",
		"dial tcp 127.0.0.1:1: connection refused", rpc.Framing{})
	if Classify(failed) != Broken || Classify(dialed) != Broken {
		t.Fatalf("both failures must be Broken: %v %v", Classify(failed), Classify(dialed))
	}
	sev1, _, msg1, _ := ClickNotice(failed)
	sev2, _, msg2, _ := ClickNotice(dialed)
	if sev1 != errsurface.Error || sev2 != errsurface.Error {
		t.Errorf("severities = %v %v, want both errsurface.Error", sev1, sev2)
	}
	if !strings.Contains(msg1, "plugin not responding: boom") {
		t.Errorf("message = %q, want the recorded failure as the reason", msg1)
	}
	if !strings.Contains(msg2, "connection refused") {
		t.Errorf("message = %q, want the recorded failure as the reason", msg2)
	}
	if !strings.HasPrefix(msg1, "Files: ") || !strings.HasPrefix(msg2, "Files: ") {
		t.Errorf("messages = %q / %q, want one shape: the label, then the reason", msg1, msg2)
	}
}

// A plugin with no doorway of its own is not an error, so a click says
// nothing.
func TestClickNotice_NoDoor_NotOk(t *testing.T) {
	pl := &gridwellv1.PluginInfo{Uuid: "u1", Label: "Mail",
		MenuEntries: []*gridwellv1.MenuEntry{{Id: "feed", Label: "Feed", GridId: "u1/2"}}}
	if _, _, _, ok := ClickNotice(pl); ok {
		t.Error("a plugin with no doorway of its own must report nothing")
	}
}

func TestClickNotice_Enterable_NotOk(t *testing.T) {
	pl := &gridwellv1.PluginInfo{Label: "Home", RootGridId: "u/1"}
	_, _, _, ok := ClickNotice(pl)
	if ok {
		t.Error("ClickNotice for an enterable plugin must return ok=false (caller should descend)")
	}
}

func TestClickNotice_KeyedByUUID(t *testing.T) {
	pl := &gridwellv1.PluginInfo{Uuid: "uux1", Label: "Files", InfoError: "plugin not responding: boom"}
	_, source, _, ok := ClickNotice(pl)
	if !ok {
		t.Fatal("ClickNotice for a broken plugin must return ok=true")
	}
	if source != "launcher:uux1" {
		t.Errorf("source = %q, want launcher:uux1 (the UUID — labels can collide)", source)
	}
}

// Repeated clicks on one plugin share a source key, so errsurface updates one
// row rather than scrolling the strip.
func TestClickNotice_SourceKeyedByLabelCoalesces(t *testing.T) {
	pl := &gridwellv1.PluginInfo{Label: "Files", InfoError: "boom"}
	_, s1, _, _ := ClickNotice(pl)
	_, s2, _, _ := ClickNotice(pl)
	if s1 != s2 {
		t.Errorf("source key changed across identical clicks: %q vs %q", s1, s2)
	}
}

// A connection row is recognized by its declared kind, not its uuid shape, so
// this case is built through rpc.ConnectionRow, the one minter.
func TestClickNotice_PendingConnection(t *testing.T) {
	pl := rpc.ConnectionRow("conn1", "rtb", "", "", rpc.Framing{})
	if _, _, msg, _ := ClickNotice(pl); !strings.Contains(msg, "loading rtb") {
		t.Fatalf("an unsegmented connection uuid must still read as loading: %q", msg)
	}
	pl = rpc.ConnectionRow("sshx/conn1", "rtb", "", "", rpc.Framing{})
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
	pl.InfoError = "dial tcp 127.0.0.1:1: connection refused"
	sev, _, msg, ok = ClickNotice(pl)
	if !ok || sev != errsurface.Error || !strings.Contains(msg, "connection refused") {
		t.Fatalf("dial detail must surface as an error: %v %q ok=%v", sev, msg, ok)
	}
}

func TestUnrootedLink(t *testing.T) {
	cases := []struct {
		name string
		tile *gridwellv1.Tile
		want bool
	}{
		{"launcher with no root", &gridwellv1.Tile{Kind: rpc.KindWell, Reference: true}, true},
		{"launcher with a root", &gridwellv1.Tile{Kind: rpc.KindWell, Reference: true, ChildGridId: "fs/1"}, false},
		{"leaf link", &gridwellv1.Tile{Kind: rpc.KindText, Reference: true, LinkTargetId: "fs/2"}, false},
		{"plain well", &gridwellv1.Tile{Kind: rpc.KindWell, ChildGridId: "3"}, false},
	}
	for _, c := range cases {
		if got := UnrootedLink(c.tile); got != c.want {
			t.Errorf("%s: UnrootedLink = %v, want %v", c.name, got, c.want)
		}
	}
}
