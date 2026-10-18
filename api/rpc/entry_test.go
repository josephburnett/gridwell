package rpc

import "testing"

// An entry's address names its grid and key and nothing else, whatever
// bytes either holds, and a grid's address is never an entry's.
func TestEntryIDRoundTrip(t *testing.T) {
	for _, c := range []struct{ grid, key string }{
		{"box:imbox", "thread:42"},
		{"/home/joe", "/home/joe/a b/c.md"},
		{"", "k"},
	} {
		ctx, key, isTile, ok := SplitEntryID(EntryTileID(c.grid, c.key))
		if !ok || !isTile || ctx != c.grid || key != c.key {
			t.Errorf("SplitEntryID(EntryTileID(%q, %q)) = %q, %q, %v, %v", c.grid, c.key, ctx, key, isTile, ok)
		}
		ctx, _, isTile, ok = SplitEntryID(EntryGridID(c.grid))
		if !ok || isTile || ctx != c.grid {
			t.Errorf("SplitEntryID(EntryGridID(%q)) = %q, tile %v, ok %v", c.grid, ctx, isTile, ok)
		}
	}
}
