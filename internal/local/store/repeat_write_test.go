package store

import (
	"context"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store/storetest"
)

// repeatWritesNothing names the versionCases a second identical call leaves
// alone: a write that lands where the row already is never mutates, not even
// updated_at, and tells no subscriber, because every view would re-read and
// re-settle against it. The rest claim a version the first call spent, or make
// rows.
var repeatWritesNothing = map[string]bool{
	"SetTextScroll/scroll and mode": true,
	"SetContentZoom/content scale":  true,
	"SetFrozen/standing freeze":     true,
	"SetFraming/door viewport":      true,
	"MoveTile/move and resize":      true,
	"SetTileAlt/automatic capture":  true,
	"SetURLState/freeze capture":    true,
	"SetShellPreview/frozen frame":  true,
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
			before := storetest.DumpOf(t, s.db)
			if err := c.mutate(t, s, ctx, tile); err != nil {
				t.Fatalf("repeat: %v", err)
			}
			if ch := storetest.Changed(before, storetest.DumpOf(t, s.db)); len(ch) > 0 {
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

// A root has no tile row, so its framing lands on the grid row and announces
// itself; repeating it must write and announce nothing, as a door's does.
func TestARepeatedRootFramingWritesNothing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	sentinel, err := s.CreateShell(ctx, root, 40, 40, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	req := &gridwellv1.SetFramingRequest{RootGridId: root, Cx: 3.5, Cy: -2.25, Zoom: 1.75}
	if _, err := s.SetFraming(ctx, req); err != nil {
		t.Fatalf("first: %v", err)
	}
	s.SetClock(func() time.Time { return time.Unix(2_000_000_000, 0) })
	events, cancel := s.SubscribeEvents()
	defer cancel()
	before := storetest.DumpOf(t, s.db)
	if _, err := s.SetFraming(ctx, req); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if ch := storetest.Changed(before, storetest.DumpOf(t, s.db)); len(ch) > 0 {
		t.Errorf("the repeat wrote %v", ch)
	}
	if err := s.SetTileAlt(ctx, sentinel.Id, "sentinel", false); err != nil {
		t.Fatal(err)
	}
	for ev := range events {
		if ev.GetTileChanged().GetTile().GetId() == sentinel.Id {
			break
		}
		t.Errorf("the repeat told subscribers: %v", ev)
	}
}

// A plugin row's writes say whether they changed it, because the adapter
// announces them itself; a repeat changes nothing, not even updated_at, and
// says so (pluginhost.TestARepeatedPluginWriteAnnouncesNothing is the other
// side of the seam).
func TestARepeatedPluginRowWriteWritesNothing(t *testing.T) {
	s, d := openExt(t)
	gid, err := d.ContextID("inbox")
	if err != nil {
		t.Fatal(err)
	}
	text, err := d.Mint(gid, &pluginv1.Entry{Key: "a", Kind: rpc.KindText, Label: "a"}, 0, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	page, err := d.Mint(gid, &pluginv1.Entry{Key: "u", Kind: rpc.KindURL, Label: "u", UrlString: "https://example.com/"}, 0, 2, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	f, err := rpc.NewFraming(1, 2, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct {
		name  string
		write func() (bool, error)
	}{
		{"move", func() (bool, error) { return d.Move(text, 3, 3, 1, 1, nil) }},
		{"tile framing", func() (bool, error) { return d.SetFraming(text, 0, f) }},
		{"root framing", func() (bool, error) { return d.SetFraming(0, gid, f) }},
		{"text scroll", func() (bool, error) { return d.SetTextScroll(text, 0, 0, 4, 4, rpc.TextModeRendered) }},
		{"content zoom", func() (bool, error) { return d.SetContentZoom(text, zoomOf(1.25)) }},
		{"standing freeze", func() (bool, error) { return d.SetFrozen(page, true, nil) }},
		{"screenshot", func() (bool, error) { return d.SetURLPreview(page, []byte("shot")) }},
	} {
		t.Run(w.name, func(t *testing.T) {
			s.SetClock(func() time.Time { return time.Unix(1_900_000_000, 0) })
			if changed, err := w.write(); err != nil || !changed {
				t.Fatalf("first = %v, %v; want a change", changed, err)
			}
			s.SetClock(func() time.Time { return time.Unix(2_000_000_000, 0) })
			before := storetest.DumpOf(t, s.db)
			changed, err := w.write()
			if err != nil || changed {
				t.Fatalf("repeat = %v, %v; want no change", changed, err)
			}
			if ch := storetest.Changed(before, storetest.DumpOf(t, s.db)); len(ch) > 0 {
				t.Errorf("the repeat wrote %v", ch)
			}
		})
	}
}
