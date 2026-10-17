package store

import (
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
