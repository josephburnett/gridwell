package textedit

import "testing"

// The two flush paths hold different ids for a linked document and must name
// one chain, or the same edit is saved twice under one basis.
func TestSaveQueueKeyIsOnePerDocument(t *testing.T) {
	for _, tc := range []struct {
		name            string
		viewed, content string
		want            string
	}{
		{"a text row is its own document", "t1", "t1", "t1"},
		{"a leaf link rides its target's chain", "link1", "t1", "t1"},
		{"an unresolved owner row falls back to itself", "t1", "", "t1"},
	} {
		if got := SaveQueueKey(tc.viewed, tc.content); got != tc.want {
			t.Errorf("%s: SaveQueueKey(%q, %q) = %q, want %q", tc.name, tc.viewed, tc.content, got, tc.want)
		}
	}
	ascent := SaveQueueKey("link1", "t1")
	debounce := SaveQueueKey("t1", "t1")
	if ascent != debounce {
		t.Errorf("a linked document got two chains: ascent flush %q, debounce flush %q", ascent, debounce)
	}
}
