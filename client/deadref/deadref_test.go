package deadref

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

const (
	node = "n1abcde"
	fs   = "fs9xyzw"
	gone = "z9gonee"
)

// roster is the handshake's shape: home, one plugin, one connection whose UUID
// is "<node>/<name>".
func roster() []*gridwellv1.PluginInfo {
	return []*gridwellv1.PluginInfo{
		{Uuid: node, Label: "home"},
		{Uuid: fs, Label: "files"},
		{Uuid: node + "/laptop", Kind: rpc.PluginKindConnection, Label: "laptop"},
	}
}

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

// Deeper segments name the far node's plugins, which this roster never lists.
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

// Before the handshake lands the roster is empty and every link would read
// dead.
func TestAnEmptyRosterJudgesNothing(t *testing.T) {
	if Dead(gone+"/1", nil, node) {
		t.Error("an empty roster must judge nothing: the handshake has not landed yet")
	}
	if Dead(gone+"/1", []*gridwellv1.PluginInfo{}, node) {
		t.Error("an empty roster must judge nothing")
	}
}

func TestTargetIDCoversBothLinkShapesAndOnlyLinks(t *testing.T) {
	cases := []struct {
		name string
		tile *gridwellv1.Tile
		want string
	}{
		{"a well link carries its child grid",
			&gridwellv1.Tile{Kind: rpc.KindWell, Reference: true, ChildGridId: fs + "/1"}, fs + "/1"},
		{"a leaf link carries its target",
			&gridwellv1.Tile{Kind: rpc.KindText, Reference: true, LinkTargetId: fs + "/42"}, fs + "/42"},
		{"an owned interior well is not a link",
			&gridwellv1.Tile{Kind: rpc.KindWell, ChildGridId: node + "/9"}, ""},
		{"an owned text tile is not a link",
			&gridwellv1.Tile{Kind: rpc.KindText}, ""},
		{"a childless reference is a menu swatch, not a link into anywhere",
			&gridwellv1.Tile{Kind: rpc.KindWell, Reference: true}, ""},
	}
	for _, c := range cases {
		tile := c.tile
		if got := TargetID(tile); got != c.want {
			t.Errorf("%s: TargetID = %q, want %q", c.name, got, c.want)
		}
	}
	if TargetID(nil) != "" {
		t.Error("TargetID(nil) must be empty")
	}
}

func TestDeadTile(t *testing.T) {
	dead := &gridwellv1.Tile{Kind: rpc.KindWell, Reference: true, ChildGridId: gone + "/1"}
	if !DeadTile(dead, roster(), node) {
		t.Error("a link into an undeclared namespace is dead")
	}
	live := &gridwellv1.Tile{Kind: rpc.KindWell, Reference: true, ChildGridId: fs + "/1"}
	if DeadTile(live, roster(), node) {
		t.Error("a link into a declared plugin is alive, whatever that plugin's health")
	}
	owned := &gridwellv1.Tile{Kind: rpc.KindWell, ChildGridId: gone + "/1"}
	if DeadTile(owned, roster(), node) {
		t.Error("an owned well is not a link and has no dead verdict")
	}
}
