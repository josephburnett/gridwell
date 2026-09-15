package textedit

import "testing"

func TestDecideFlush(t *testing.T) {
	cases := []struct {
		name                                   string
		rowKnown, rowEditableText, loadRefused bool
		want                                   Flush
	}{
		{"editable text posts", true, true, false, FlushPost},
		{"uncached row is fetched", false, false, false, FlushFetchRow},
		{"refused row is reported", false, false, true, FlushNoRow},
		// A row's writability is a server fact that can change under bytes
		// already typed: a plugin grid that answers read-only, or a grid that
		// falls out of the cache. Before this arm the sweep returned here, so
		// the entry stayed parked dirty and re-ran the same no-op forever
		// with nothing said.
		{"unwritable row is reported", true, false, false, FlushUnwritable},
		{"a refused load cannot mask a known row", true, false, true, FlushUnwritable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DecideFlush(c.rowKnown, c.rowEditableText, c.loadRefused); got != c.want {
				t.Errorf("DecideFlush = %v, want %v", got, c.want)
			}
		})
	}
}
