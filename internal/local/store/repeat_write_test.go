package store

import (
	"context"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/internal/local/store/storetest"
)

// repeatWritesNothing names the versionCases a second identical call leaves
// alone: a write that lands where the row already is never mutates, not even
// updated_at, and tells no subscriber, because every view would re-read and
// re-settle against it. The rest claim a version the first call spent, or make
// rows.
var repeatWritesNothing = map[string]bool{
	"SetTextView/window and mode":         true,
	"SetContentZoom/content scale":        true,
	"SetFrozen/standing freeze":           true,
	"SetFraming/doorway viewport":         true,
	"SetPaneLayout/workspace arrangement": true,
	"PlaceTile/move and resize":           true,
	"SetTileAlt/automatic capture":        true,
	"SetURLState/freeze capture":          true,
	"SetShellPreview/frozen frame":        true,
}

func TestARepeatedWriteWritesNothing(t *testing.T) {
	for _, c := range versionCases {
		if !repeatWritesNothing[c.name] {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			root := rootID(t, s)
			tile := c.subject(t, s, ctx, root)
			sentinel, err := s.CreateShell(ctx, root, 40, 40, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.mutate(t, s, ctx, tile); err != nil {
				t.Fatalf("first: %v", err)
			}
			s.SetClock(func() time.Time { return time.Unix(2_000_000_000, 0) })
			events, cancel := s.SubscribeEvents()
			defer cancel()
			before := storetest.DumpOf(t, s.SQL())
			if err := c.mutate(t, s, ctx, tile); err != nil {
				t.Fatalf("repeat: %v", err)
			}
			if ch := storetest.Changed(before, storetest.DumpOf(t, s.SQL())); len(ch) > 0 {
				t.Errorf("the repeat wrote %v", ch)
			}
			if err := s.SetTileAlt(ctx, sentinel.Id, "sentinel", false); err != nil {
				t.Fatal(err)
			}
			for ev := range events {
				id := ev.GetTileChanged().GetTile().GetId()
				if id == sentinel.Id {
					break
				}
				t.Errorf("the repeat told subscribers: %v", ev)
			}
		})
	}
}
