package store

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/josephburnett/gridwell/internal/trace"
)

// Each step a migration ran is one record with the versions and the rows it
// wrote, only once the chain commits: a dump otherwise cannot tell a boot that
// rewrote the file from one that read it.
func TestEachMigrationStepIsTracedWithItsRows(t *testing.T) {
	ctx := context.Background()
	db, root := buildDBAtV1(t, filepath.Join(t.TempDir(), "traced.db"))
	seedV1(t, db, root)
	var tiles int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tiles`).Scan(&tiles); err != nil {
		t.Fatal(err)
	}
	if tiles == 0 {
		t.Fatal("the v1 seed holds no tiles")
	}
	mark := newestSeq()

	migs := []migration{
		{to: 2, run: addColumn(`ALTER TABLE tiles ADD COLUMN note TEXT`)},
		{to: 3, run: addColumn(`UPDATE tiles SET note = 'n'`)},
	}
	if err := storeOver(db).migrateUp(ctx, migs, 3); err != nil {
		t.Fatal(err)
	}
	var got []map[string]string
	for _, rec := range trace.Default().Snapshot() {
		if rec.Seq > mark && rec.Src == "store" && rec.Kind == "migrate" {
			got = append(got, rec.KV)
		}
	}
	want := []map[string]string{{"from": "1", "to": "2", "rows": "0"}, {"from": "2", "to": "3", "rows": strconv.Itoa(tiles)}}
	if len(got) != len(want) {
		t.Fatalf("migrate records = %v, want %v", got, want)
	}
	for i := range want {
		for k, v := range want[i] {
			if got[i][k] != v {
				t.Errorf("record %d = %v, want %v", i, got[i], want[i])
			}
		}
	}

	// A file already at the target runs nothing and says nothing.
	mark = newestSeq()
	if err := storeOver(db).migrateUp(ctx, migs, 3); err != nil {
		t.Fatal(err)
	}
	for _, rec := range trace.Default().Snapshot() {
		if rec.Seq > mark && rec.Src == "store" && rec.Kind == "migrate" {
			t.Errorf("a file at its target traced %v", rec)
		}
	}
}

func newestSeq() uint64 {
	recs := trace.Default().Snapshot()
	if len(recs) == 0 {
		return 0
	}
	return recs[len(recs)-1].Seq
}
