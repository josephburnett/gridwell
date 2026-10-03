package rpc

import "testing"

// An entry's address names its context and key and nothing else, whatever
// bytes either holds, and a context's address is never an entry's.
func TestEntryIDRoundTrip(t *testing.T) {
	for _, c := range []struct{ context, key string }{
		{"box:imbox", "thread:42"},
		{"/home/joe", "/home/joe/a b/c.md"},
		{"", "k"},
	} {
		ctx, key, isTile, ok := SplitEntryID(EntryTileID(c.context, c.key))
		if !ok || !isTile || ctx != c.context || key != c.key {
			t.Errorf("SplitEntryID(EntryTileID(%q, %q)) = %q, %q, %v, %v", c.context, c.key, ctx, key, isTile, ok)
		}
		ctx, _, isTile, ok = SplitEntryID(EntryGridID(c.context))
		if !ok || isTile || ctx != c.context {
			t.Errorf("SplitEntryID(EntryGridID(%q)) = %q, tile %v, ok %v", c.context, ctx, isTile, ok)
		}
	}
}
