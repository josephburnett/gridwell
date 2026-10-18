package store

import (
	"context"
	"testing"
	"time"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// rowTimes reads a tile row's created_at and updated_at.
func rowTimes(t *testing.T, s *Store, id int64) (created, updated int64) {
	t.Helper()
	if err := s.db.QueryRow(`SELECT created_at, updated_at FROM tiles WHERE id = ?`, id).
		Scan(&created, &updated); err != nil {
		t.Fatal(err)
	}
	return created, updated
}

// A plugin's rows keep time in the unit home's do, unix seconds, so one
// filter reads every row of the table.
func TestPluginRowTimesAreUnixSeconds(t *testing.T) {
	st, ns := openExt(t)
	at := time.Unix(1_800_000_000, 123)
	st.SetClock(func() time.Time { return at })
	gid, err := ns.ContextID("c")
	if err != nil {
		t.Fatal(err)
	}
	id, err := ns.Mint(gid, &pluginv1.Entry{Key: "k", Kind: "text", Label: "k"}, 0, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if c, u := rowTimes(t, st, id); c != at.Unix() || u != at.Unix() {
		t.Errorf("minted row times = %d, %d, want %d", c, u, at.Unix())
	}
	var gc, gu int64
	if err := st.db.QueryRow(`SELECT created_at, updated_at FROM grids WHERE id = ?`, gid).Scan(&gc, &gu); err != nil {
		t.Fatal(err)
	}
	if gc != at.Unix() || gu != at.Unix() {
		t.Errorf("context grid times = %d, %d, want %d", gc, gu, at.Unix())
	}
	later := at.Add(time.Hour)
	st.SetClock(func() time.Time { return later })
	f, err := rpc.NewFraming(1, 2, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ns.SetFraming(id, 0, f); err != nil {
		t.Fatal(err)
	}
	if _, u := rowTimes(t, st, id); u != later.Unix() {
		t.Errorf("framed row updated_at = %d, want %d", u, later.Unix())
	}
}

// Every write to a plugin's row moves its updated_at, as every write to
// home's does: a move, a text scroll, a content zoom.
func TestPluginRowWritesStampUpdatedAt(t *testing.T) {
	st, ns := openExt(t)
	at := time.Unix(1_800_000_000, 0)
	st.SetClock(func() time.Time { return at })
	gid, err := ns.ContextID("c")
	if err != nil {
		t.Fatal(err)
	}
	id, err := ns.Mint(gid, &pluginv1.Entry{Key: "k", Kind: "text", Label: "k"}, 0, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	writes := []struct {
		name  string
		write func() (bool, error)
	}{
		{"Move", func() (bool, error) { return ns.Move(id, 4, 4, 2, 2, nil) }},
		{"SetTextScroll", func() (bool, error) { return ns.SetTextScroll(id, 10, 20, 0, 0, "rendered") }},
		{"SetContentZoom", func() (bool, error) { return ns.SetContentZoom(id, zoomOf(1.5)) }},
	}
	for i, w := range writes {
		now := at.Add(time.Duration(i+1) * time.Hour)
		st.SetClock(func() time.Time { return now })
		if changed, err := w.write(); err != nil || !changed {
			t.Fatalf("%s: changed=%v err=%v", w.name, changed, err)
		}
		if _, u := rowTimes(t, st, id); u != now.Unix() {
			t.Errorf("%s: updated_at = %d, want %d", w.name, u, now.Unix())
		}
	}
}

// contentChangedAt reads a tile row's content time the way the find filter
// does (contentAtSQL).
func contentChangedAt(t *testing.T, s *Store, tileID string) int64 {
	t.Helper()
	var at int64
	if err := s.db.QueryRow(`SELECT `+contentAtSQL+` FROM tiles t WHERE t.id = ?`, mustParseID(t, tileID)).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

// content_at moves exactly where version does: every case of the version
// rule's table that bumps moves it, and every one that does not (a capture,
// framing, a move, a freeze, a selection) leaves it at the row's creation.
func TestContentAtFollowsVersion(t *testing.T) {
	for _, c := range versionCases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			created := time.Unix(1_800_000_000, 0)
			s.SetClock(func() time.Time { return created })
			tile := c.subject(t, s, ctx, rootID(t, s))
			later := created.Add(time.Hour)
			s.SetClock(func() time.Time { return later })
			if err := c.mutate(t, s, ctx, tile); err != nil {
				t.Fatalf("mutate: %v", err)
			}
			want := created.Unix()
			if c.bumps {
				want = later.Unix()
			}
			if got := contentChangedAt(t, s, tile.Id); got != want {
				t.Errorf("content time = %d, want %d (bumps=%v)", got, want, c.bumps)
			}
		})
	}
}
