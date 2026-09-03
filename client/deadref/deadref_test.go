package deadref

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

const (
	node = "n1abcde"
	fs   = "fs9xyzw"
	gone = "z9gonee"
)

// roster is the node as the handshake declares it: home, one plugin, one
// connection. It is exactly rpc.MenuRows' shape — a connection's UUID is
// "<node>/<name>".
func roster() []rpc.PluginInfo {
	return []rpc.PluginInfo{
		{UUID: node, Label: "home"},
		{UUID: fs, Label: "files"},
		{UUID: node + "/laptop", Kind: rpc.PluginKindConnection, Label: "laptop"},
	}
}

// The boundary itself: a namespace the node declares is alive whatever state
// it is in, and one it does not declare is dead. Every case here is an id a
// real link tile stores.
func TestDeadIsDeclaredNessAndNothingElse(t *testing.T) {
	cases := []struct {
		name string
		id   string
		dead bool
	}{
		{"a declared plugin's grid", fs + "/1", false},
		{"a declared plugin's key-form tile", fs + "/" + rpc.KeyTileID("/home/joe"), false},
		{"the node's own home grid", node + "/7", false},
		{"a declared connection", node + "/laptop/far9xyz/1", false},
		{"a plugin removed from server.yaml", gone + "/1", true},
		{"a connection name no longer declared", node + "/oldbox/far9xyz/1", true},
		{"a bare, unqualified id", "7", false},
		{"no id at all", "", false},
	}
	for _, c := range cases {
		if got := Dead(c.id, roster(), node); got != c.dead {
			t.Errorf("%s: Dead(%q) = %v, want %v", c.name, c.id, got, c.dead)
		}
	}
}

// A link through a DECLARED connection is never judged here, however deep it
// chains: those segments name the far node's own plugins, which only the far
// node declares. Judging them against this node's roster would grey every
// mounted tile the moment a remote used a plugin this node happens not to
// have.
func TestAFarNodesNamespacesAreNotThisNodesToJudge(t *testing.T) {
	for _, id := range []string{
		node + "/laptop/" + gone + "/1",
		node + "/laptop/far9xyz/deeper/1",
	} {
		if Dead(id, roster(), node) {
			t.Errorf("Dead(%q) = true; a chain through a declared connection is the far node's to judge", id)
		}
	}
}

// The verdict must never fire on absence of knowledge. Before the handshake
// lands the roster is empty and every link would read dead, which would grey
// the whole grid for a blink on every boot.
func TestAnEmptyRosterJudgesNothing(t *testing.T) {
	if Dead(gone+"/1", nil, node) {
		t.Error("an empty roster must judge nothing: the handshake has not landed yet")
	}
	if Dead(gone+"/1", []rpc.PluginInfo{}, node) {
		t.Error("an empty roster must judge nothing")
	}
}

// TargetID reads Reference, the node's own derived "is a link" bit, and
// covers both link shapes. A tile that is not a link has no target, so no
// verdict.
func TestTargetIDCoversBothLinkShapesAndOnlyLinks(t *testing.T) {
	cases := []struct {
		name string
		tile rpc.Tile
		want string
	}{
		{"a well link carries its child grid",
			rpc.Tile{Kind: rpc.KindWell, Reference: true, ChildGridID: fs + "/1"}, fs + "/1"},
		{"a leaf link carries its target",
			rpc.Tile{Kind: rpc.KindText, Reference: true, LinkTargetID: fs + "/42"}, fs + "/42"},
		{"an owned interior well is not a link",
			rpc.Tile{Kind: rpc.KindWell, ChildGridID: node + "/9"}, ""},
		{"an owned text tile is not a link",
			rpc.Tile{Kind: rpc.KindText}, ""},
		{"a childless reference is a menu swatch, not a link into anywhere",
			rpc.Tile{Kind: rpc.KindWell, Reference: true}, ""},
	}
	for _, c := range cases {
		tile := c.tile
		if got := TargetID(&tile); got != c.want {
			t.Errorf("%s: TargetID = %q, want %q", c.name, got, c.want)
		}
	}
	if TargetID(nil) != "" {
		t.Error("TargetID(nil) must be empty")
	}
}

// DeadTile is the two joined: an owned tile is never dead however missing
// its ids look, and a link into a missing namespace is.
func TestDeadTile(t *testing.T) {
	dead := rpc.Tile{Kind: rpc.KindWell, Reference: true, ChildGridID: gone + "/1"}
	if !DeadTile(&dead, roster(), node) {
		t.Error("a link into an undeclared namespace is dead")
	}
	live := rpc.Tile{Kind: rpc.KindWell, Reference: true, ChildGridID: fs + "/1"}
	if DeadTile(&live, roster(), node) {
		t.Error("a link into a declared plugin is alive, whatever that plugin's health")
	}
	owned := rpc.Tile{Kind: rpc.KindWell, ChildGridID: gone + "/1"}
	if DeadTile(&owned, roster(), node) {
		t.Error("an owned well is not a link and has no dead verdict")
	}
}
