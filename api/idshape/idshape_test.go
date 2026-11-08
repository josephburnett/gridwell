package idshape

import (
	"strconv"
	"testing"
)

func TestNewShortIDShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewShortID()
		if len(id) != shortIDLen {
			t.Fatalf("len(%q) = %d, want %d", id, len(id), shortIDLen)
		}
		if id[0] < 'a' || id[0] > 'z' {
			t.Fatalf("%q does not start with a letter — it could be purely numeric", id)
		}
		for j := 0; j < len(id); j++ {
			c := id[j]
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')) {
				t.Fatalf("%q contains %q outside lowercase base36", id, c)
			}
		}
		if _, err := strconv.ParseInt(id, 10, 64); err == nil {
			t.Fatalf("%q parses as an integer — indistinguishable from a tile id", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q within 1000 draws", id)
		}
		seen[id] = true
	}
}

// NewUUID mints the store's node uuid, which claims uniqueness across nodes.
func TestNewUUIDStays128Bit(t *testing.T) {
	id := NewUUID()
	if len(id) != 32 {
		t.Fatalf("NewUUID() = %q (len %d), want 32 hex chars", id, len(id))
	}
}

// A nameless connection stanza would occupy the empty segment, so
// "<node>//12" would peel to it and read as the node's own id.
func TestValidateSegmentRefusesTheEmptySegment(t *testing.T) {
	if err := ValidateSegment("connection name", ""); err == nil {
		t.Fatal("an empty segment must be refused")
	}
}
