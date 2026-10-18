package pluginhost_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/plugintest/gitlabfake"
)

// The filters are the node's, never the plugin's: through the real gitlab
// binary, a grid scope reads the plugin's grid path, and a time or the
// selection reads the node's row for the entry, so an entry the user never
// touched, having no row, passes neither.
func TestFindFiltersReachAPluginsRows(t *testing.T) {
	gl := gitlabfake.New(t,
		gitlabTodo(1, "2026-08-18T10:00:00Z", "fix the widget", "please review the widget"),
		gitlabTodo(2, "2026-08-19T10:00:00Z", "a widget gadget", "no rush"),
	)
	hs := gitlabServer(t, gl)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	raw := gridwellv1connect.NewGridwellClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	ctx := context.Background()
	find := func(req *gridwellv1.FindRequest) []string {
		t.Helper()
		resp, err := raw.Find(ctx, connect.NewRequest(req))
		if err != nil {
			t.Fatalf("find %v: %v", req, err)
		}
		var alts []string
		for _, r := range resp.Msg.Results {
			alts = append(alts, r.Tile.AltText)
		}
		return alts
	}
	has := func(alts []string, sub ...string) bool {
		if len(alts) != len(sub) {
			return false
		}
		for i := range sub {
			if !strings.Contains(alts[i], sub[i]) {
				return false
			}
		}
		return true
	}

	pl, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootID := plugintest.LandingOf(t, pl.Plugins[0])
	root, err := cl.GetGrid(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	var week *gridwellv1.Tile
	for _, tile := range root.Tiles {
		if strings.HasPrefix(tile.AltText, "2026-08-17") {
			week = tile
		}
	}
	if week == nil {
		t.Fatalf("no week door in %v", root.Tiles)
	}

	if got := find(&gridwellv1.FindRequest{Query: "widget", GridId: rootID}); len(got) != 0 {
		t.Errorf("widget on the root alone = %v, want none: the todos are a week down", got)
	}
	if got := find(&gridwellv1.FindRequest{Query: "widget", GridId: rootID, Recurse: true}); len(got) != 2 {
		t.Errorf("widget under the root = %v, want both todos", got)
	}
	if got := find(&gridwellv1.FindRequest{Query: "widget", Created: &gridwellv1.TimeRange{From: 1}}); len(got) != 0 {
		t.Errorf("widget with a created filter = %v, want none: no todo has a row yet", got)
	}

	// Selecting the second todo mints its row: now it has a time and is in
	// its grid's selection.
	weekGrid, err := cl.GetGrid(ctx, week.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	var second *gridwellv1.Tile
	for _, tile := range weekGrid.Tiles {
		if strings.Contains(tile.AltText, "!2") {
			second = tile
		}
	}
	if second == nil {
		t.Fatalf("no !2 in %v", weekGrid.Tiles)
	}
	before := time.Now().Unix()
	if err := cl.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: week.ChildGridId,
		Add: []string{second.Id}}); err != nil {
		t.Fatal(err)
	}
	if got := find(&gridwellv1.FindRequest{Query: "widget", Selected: true}); !has(got, "!2") {
		t.Errorf("selected widget = %v, want !2", got)
	}
	if got := find(&gridwellv1.FindRequest{Selected: true}); !has(got, "!2") {
		t.Errorf("selected, no query = %v, want !2", got)
	}
	// Found by its row alone, it is still a place: the path down to it is
	// listed from the grid through the grids the node holds.
	r, err := raw.Find(ctx, connect.NewRequest(&gridwellv1.FindRequest{Selected: true}))
	if err != nil {
		t.Fatal(err)
	}
	if p := r.Msg.Results[0].Path; len(p) != 1 || p[0].Id != week.Id {
		t.Errorf("path = %v, want the week door", p)
	}
	if got := find(&gridwellv1.FindRequest{Query: "widget", Created: &gridwellv1.TimeRange{From: before}}); !has(got, "!2") {
		t.Errorf("widget made since the select = %v, want !2, the one row", got)
	}
	if got := find(&gridwellv1.FindRequest{Selected: true, GridId: rootID}); len(got) != 0 {
		t.Errorf("selected on the root alone = %v, want none", got)
	}
	// A kind is the node's filter too, and an entry with no row has its kind:
	// both todos pass their own, and none passes another.
	if got := find(&gridwellv1.FindRequest{Query: "widget", Kind: second.Kind}); len(got) != 2 {
		t.Errorf("widget of kind %s = %v, want both todos, one with no row", second.Kind, got)
	}
	if got := find(&gridwellv1.FindRequest{Query: "widget", Kind: rpc.KindWorkspace}); len(got) != 0 {
		t.Errorf("widget of kind workspace = %v, want none", got)
	}
	if got := find(&gridwellv1.FindRequest{GridId: rootID, Recurse: true, Kind: rpc.KindDoor}); len(got) == 0 || slices.ContainsFunc(got, func(a string) bool { return strings.Contains(a, "widget") }) {
		t.Errorf("the doors under the root = %v, want the week doors and no todo", got)
	}
	got := find(&gridwellv1.FindRequest{GridId: week.ChildGridId})
	if len(got) != 2 || !strings.Contains(got[0], "!2") {
		t.Errorf("the week listed = %v, want both, the one with a row first", got)
	}
}
