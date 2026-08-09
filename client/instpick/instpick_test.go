package instpick

import (
	"testing"

	"github.com/josephburnett/gridwell/client/schemaform"
	"github.com/josephburnett/gridwell/internal/rpc"
)

func sshForm(t *testing.T) *schemaform.Form {
	t.Helper()
	f, err := schemaform.Parse(`{
		"type": "object",
		"properties": {
			"host": {"type": "string"},
			"user": {"type": "string"},
			"port": {"type": "number"},
			"key":  {"type": "string", "format": "secret"}
		},
		"required": ["host", "user"]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestEntryStatus(t *testing.T) {
	cases := []struct {
		e    Entry
		want Status
	}{
		{Entry{ParamsJSON: `{"host":"h"}`, ChildGridID: "s/c/r/0"}, Ready},
		{Entry{ParamsJSON: `{"host":"h"}`}, Pending},
		{Entry{}, Inert},
	}
	for _, c := range cases {
		if got := c.e.Status(); got != c.want {
			t.Errorf("Status(%+v) = %v, want %v", c.e, got, c.want)
		}
	}
}

func TestBuildEntries_WellsOnlySortedNamedFirst(t *testing.T) {
	tiles := []rpc.Tile{
		{ID: "s/3", Kind: rpc.KindWell, AltText: ""},
		{ID: "s/2", Kind: rpc.KindWell, AltText: "Zeta"},
		{ID: "s/9", Kind: rpc.KindText, AltText: "not-a-well"},
		{ID: "s/1", Kind: rpc.KindWell, AltText: "alpha"},
	}
	got := BuildEntries(tiles, func(string) string { return "" })
	if len(got) != 3 {
		t.Fatalf("entries = %d, want 3 (wells only)", len(got))
	}
	if got[0].Name != "alpha" || got[1].Name != "Zeta" || got[2].TileID != "s/3" {
		t.Errorf("order = [%s %s %s], want case-insensitive names first, unnamed last",
			got[0].Name, got[1].Name, got[2].TileID)
	}
}

func TestSummary_SkipsSecretsAndEmpties(t *testing.T) {
	got := Summary(sshForm(t), `{"host":"gpu.example","user":"joe","port":2222,"key":"/home/joe/.ssh/id"}`)
	want := "host=gpu.example port=2222 user=joe"
	if got != want {
		t.Errorf("Summary = %q, want %q (form field order, no secrets)", got, want)
	}
	if s := Summary(sshForm(t), `{"host":"h","user":""}`); s != "host=h" {
		t.Errorf("Summary = %q, want empty fields skipped", s)
	}
	if s := Summary(sshForm(t), "not json"); s != "" {
		t.Errorf("Summary of garbage = %q, want \"\"", s)
	}
}

func TestMatch_CanonicalEquality(t *testing.T) {
	entries := []Entry{
		{TileID: "s/1", ParamsJSON: `{"user":"joe","host":"gpu.example"}`, ChildGridID: "s/c/r"},
		{TileID: "s/2", ParamsJSON: `{"host":"other","user":"joe"}`},
		{TileID: "s/3"}, // inert — never matches
	}
	// Key order and empty values are irrelevant.
	if m := Match(entries, []byte(`{"host":"gpu.example","user":"joe","key":""}`)); m == nil || m.TileID != "s/1" {
		t.Fatalf("Match = %+v, want s/1 (canonical equality)", m)
	}
	// Different details are a different connection.
	if m := Match(entries, []byte(`{"host":"gpu.example","user":"joe","port":2222}`)); m != nil {
		t.Errorf("Match = %+v, want nil for differing params", m)
	}
}

func TestFreeCell_PastTheRightmostTile(t *testing.T) {
	if x, y := FreeCell(nil); x != 0 || y != 0 {
		t.Errorf("empty grid cell = (%d,%d), want origin", x, y)
	}
	tiles := []rpc.Tile{
		{X: 0, Y: 2, W: 1, H: 1, Kind: rpc.KindWell},
		{X: 4, Y: 1, W: 2, H: 1, Kind: rpc.KindWell},
	}
	if x, y := FreeCell(tiles); x != 6 || y != 1 {
		t.Errorf("cell = (%d,%d), want (6,1) — one past the rightmost", x, y)
	}
}
