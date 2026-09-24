package trace

import (
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/tracewire"
)

// Two gestures that share a request id read as one gesture in the dump, and
// an id a header cannot carry never arrives at all.
func TestNewRequestIDIsUniqueAndHeaderSafe(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewRequestID()
		if seen[id] {
			t.Fatalf("duplicate request id %q within 1000 draws", id)
		}
		seen[id] = true
		if id == "" || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\r\n\"/") {
			t.Fatalf("%q cannot ride %s as written", id, tracewire.RequestHeader)
		}
	}
}

// The id is a word in a JSON record and a value in a header, so it stays in
// the shape the node's other ids already have.
func TestNewRequestIDIsShortLowercaseBase36(t *testing.T) {
	id := NewRequestID()
	if len(id) != 7 {
		t.Fatalf("NewRequestID() = %q (len %d), want 7 characters", id, len(id))
	}
	if id[0] < 'a' || id[0] > 'z' {
		t.Errorf("%q does not lead with a letter", id)
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')) {
			t.Errorf("%q contains %q outside lowercase base36", id, c)
		}
	}
}
