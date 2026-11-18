package textedit_test

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/textedit"
)

// A queued text save reads the cache entry when its turn on the chain comes:
// the save ahead of it may have landed the bytes, or been refused and taken
// them down for the row on disk. Each case plays: the body read at version 5,
// a foreign writer's event moves the row to 6 while the entry stays dirty,
// save 1 is sent, the user types more, save 2 is queued, save 1 is answered,
// and save 2 sends.
func TestAQueuedSaveSendsWhatTheEntryOwesWhenItsTurnComes(t *testing.T) {
	const cid = "n/7"
	v := func(n int64) rpc.ContentBasis { return rpc.ContentBasis{Version: n} }
	for _, tc := range []struct {
		name     string
		answer   func(c *cache.Cache)
		wantSend bool
		want     string
		claim    rpc.ContentBasis
	}{
		{"save 1 refused: the entry is gone, nothing is sent",
			func(c *cache.Cache) { c.DropTileContent(cid) }, false, "", v(0)},
		{"save 1 refused, the row on disk re-read: nothing is sent",
			func(c *cache.Cache) {
				c.DropTileContent(cid)
				c.PutFetchedContent(cid, []byte("agent"), v(6), cache.RowBasis{})
			}, false, "", v(0)},
		{"save 1 refused, typing on the re-read row: those bytes, claiming it",
			func(c *cache.Cache) {
				c.DropTileContent(cid)
				c.PutFetchedContent(cid, []byte("agent"), v(6), cache.RowBasis{})
				c.PutEditedContent(cid, []byte("agent+more"))
			}, true, "agent+more", v(6)},
		{"save 1 landed: the newer bytes, claiming what it made",
			func(c *cache.Cache) {
				c.PutSavedContent(&gridwellv1.Tile{Id: cid, Version: 7}, []byte("B1"))
			}, true, "B2", v(7)},
		{"save 1 landed, more typed since save 2 was queued: the newest bytes",
			func(c *cache.Cache) {
				c.PutEditedContent(cid, []byte("B3"))
				c.PutSavedContent(&gridwellv1.Tile{Id: cid, Version: 7}, []byte("B1"))
			}, true, "B3", v(7)},
		{"a save ahead landed save 2's bytes: nothing is sent",
			func(c *cache.Cache) {
				c.PutSavedContent(&gridwellv1.Tile{Id: cid, Version: 7}, []byte("B2"))
			}, false, "", v(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cache.New()
			c.PutFetchedContent(cid, []byte("v5"), v(5), cache.RowBasis{})
			c.PutEditedContent(cid, []byte("B1"))
			data, claim, send := textedit.SaveNow(c, cid)
			if !send || string(data) != "B1" || claim != v(5) {
				t.Fatalf("save 1 sent %q claiming %+v (send %v), want B1 claiming the base it was read at, 5", data, claim, send)
			}
			c.PutEditedContent(cid, []byte("B2"))

			tc.answer(c)

			data, claim, send = textedit.SaveNow(c, cid)
			if send != tc.wantSend {
				t.Fatalf("save 2 send = %v (%q claiming %+v), want %v", send, data, claim, tc.wantSend)
			}
			if send && (string(data) != tc.want || claim != tc.claim) {
				t.Errorf("save 2 sent %q claiming %+v, want %q claiming %+v", data, claim, tc.want, tc.claim)
			}
		})
	}
}
