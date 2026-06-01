package urldriver

import (
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

// TestPushFrameDropsOldest verifies the bounded-channel + drop-oldest
// pattern used by Session to keep frame backlog small.
func TestPushFrameDropsOldest(t *testing.T) {
	s := &Session{frames: make(chan []byte, 4)}
	for i := byte(1); i <= 5; i++ {
		s.pushFrame([]byte{i})
	}
	want := []byte{2, 3, 4, 5}
	for i, w := range want {
		got := <-s.frames
		if len(got) != 1 || got[0] != w {
			t.Errorf("frame %d = %v, want [%d]", i, got, w)
		}
	}
}

// TestPushFrameEmptyBufferAccepts confirms pushFrame just appends when
// there's room; no drop path runs.
func TestPushFrameEmptyBufferAccepts(t *testing.T) {
	s := &Session{frames: make(chan []byte, 2)}
	s.pushFrame([]byte("a"))
	s.pushFrame([]byte("b"))
	if got := string(<-s.frames); got != "a" {
		t.Errorf("first frame = %q, want \"a\"", got)
	}
	if got := string(<-s.frames); got != "b" {
		t.Errorf("second frame = %q, want \"b\"", got)
	}
}

func TestRodMouseButton(t *testing.T) {
	cases := []struct {
		in       string
		wantNone bool
	}{
		{MouseButtonLeft, false},
		{MouseButtonMiddle, false},
		{MouseButtonRight, false},
		{"unknown", true},
		{"", true},
	}
	for _, c := range cases {
		got := rodMouseButton(c.in)
		if c.wantNone {
			if got != proto.InputMouseButtonNone {
				t.Errorf("rodMouseButton(%q) = %v, want none", c.in, got)
			}
		} else if got == proto.InputMouseButtonNone {
			t.Errorf("rodMouseButton(%q) = none, want a real button", c.in)
		}
	}
}

func TestKeyText(t *testing.T) {
	cases := []struct {
		key            string
		mods           int
		text, unmodTxt string
	}{
		{"a", 0, "a", "a"},
		{"A", 0, "A", "a"},
		{"a", 2, "", ""},
		{"a", 4, "", ""},
		{"A", 1, "A", "a"},
		{"Enter", 0, "", ""},
		{"ArrowLeft", 0, "", ""},
	}
	for _, c := range cases {
		txt, ut := keyText(c.key, c.mods)
		if txt != c.text || ut != c.unmodTxt {
			t.Errorf("keyText(%q, %d) = (%q, %q), want (%q, %q)",
				c.key, c.mods, txt, ut, c.text, c.unmodTxt)
		}
	}
}

func TestVirtualKeyCode(t *testing.T) {
	cases := []struct {
		key  string
		want int
	}{
		{"a", 65}, // lowercase letter maps to its uppercase code
		{"z", 90},
		{"A", 65}, // uppercase letter passes through
		{"Z", 90},
		{"0", 48},
		{"9", 57},
		{"Enter", 13},
		{"Tab", 9},
		{"Escape", 27},
		{"ArrowLeft", 37},
		{"F1", 112},
		{"!", 0},          // unsupported single char
		{"unknown", 0},    // unsupported multi-char
		{"", 0},           // empty
	}
	for _, c := range cases {
		if got := virtualKeyCode(c.key); got != c.want {
			t.Errorf("virtualKeyCode(%q) = %d, want %d", c.key, got, c.want)
		}
	}
}
