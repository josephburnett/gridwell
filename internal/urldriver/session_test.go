package urldriver

import "testing"

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
