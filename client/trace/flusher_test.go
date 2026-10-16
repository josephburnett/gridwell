package trace

import (
	"testing"
	"time"
)

// loop is a virtual clock with a timer queue and a node that answers when the
// test says, so a test chooses how slow a post is.
type loop struct {
	now     time.Time
	dues    []time.Time
	runs    []func()
	answers []func(kept bool)
}

func (l *loop) schedule(ms int, fire func()) {
	l.dues = append(l.dues, l.now.Add(time.Duration(ms)*time.Millisecond))
	l.runs = append(l.runs, fire)
}

func (l *loop) post(_ []byte, done func(kept bool)) { l.answers = append(l.answers, done) }

// advance moves the clock and fires what came due, earliest first.
func (l *loop) advance(d time.Duration) {
	end := l.now.Add(d)
	for {
		next := -1
		for i, due := range l.dues {
			if !due.After(end) && (next < 0 || due.Before(l.dues[next])) {
				next = i
			}
		}
		if next < 0 {
			break
		}
		fire := l.runs[next]
		if l.dues[next].After(l.now) {
			l.now = l.dues[next]
		}
		l.dues = append(l.dues[:next], l.dues[next+1:]...)
		l.runs = append(l.runs[:next], l.runs[next+1:]...)
		fire()
	}
	l.now = end
}

func (l *loop) answer(kept bool) {
	done := l.answers[0]
	l.answers = l.answers[1:]
	done(kept)
}

func newLoop() (*loop, *Client, *Flusher) {
	l := &loop{now: t0}
	c := New(4096, "cid7abc")
	f := NewFlusher(c, func() time.Time { return l.now }, l.schedule, l.post)
	return l, c, f
}

func (l *loop) emit(c *Client) { c.Emit("nav", "push", "x", nil, l.now) }

// drained runs the clock for long enough that any owed record has been posted
// and answered, answering every post as it goes.
func (l *loop) drained(c *Client) bool {
	for i := 0; i < 50; i++ {
		l.advance(100 * time.Millisecond)
		for len(l.answers) > 0 {
			l.answer(true)
		}
	}
	return c.PendingCount() == 0
}

// A record that is not yet due arms a timer a window from itself. A later
// record that is due posts at once, and the timer, armed before that post,
// comes due inside its window: the pump says not yet, and a loop that does not
// arm again holds whatever that post did not carry until something else is
// emitted. A slow node (a loaded box) is what lets a record land in the gap.
func TestARecordOwedAlwaysHasAPostComing(t *testing.T) {
	l, c, _ := newLoop()
	l.advance(500 * time.Millisecond)
	l.emit(c) // inside the first window: waits for the timer
	l.advance(time.Second)
	l.answer(true) // the timer posted it
	l.advance(900 * time.Millisecond)
	l.emit(c) // 900 ms since that post: waits, timer due at 3400
	l.advance(150 * time.Millisecond)
	l.emit(c) // 1050 ms: out at once, carrying both
	if len(l.answers) != 1 {
		t.Fatalf("posts out = %d, want 1", len(l.answers))
	}
	l.advance(10 * time.Millisecond)
	l.emit(c) // emitted while that post is out
	l.advance(40 * time.Millisecond)
	l.answer(true)
	if !l.drained(c) {
		t.Fatalf("%d records were never posted", c.PendingCount())
	}
}

// One post at a time, and an idle ring holds no timer.
func TestAnIdleRingHoldsNoTimer(t *testing.T) {
	l, c, _ := newLoop()
	l.emit(c)
	if !l.drained(c) {
		t.Fatalf("%d records were never posted", c.PendingCount())
	}
	if len(l.runs) != 0 {
		t.Errorf("an acknowledged ring still holds %d timers", len(l.runs))
	}
}

// A post the node refused is retried on the clock with no further emit.
func TestARefusedPostIsRetriedWithoutAnotherEmit(t *testing.T) {
	l, c, _ := newLoop()
	l.advance(1500 * time.Millisecond)
	l.emit(c)
	l.answer(false)
	if c.PendingCount() == 0 {
		t.Fatal("a refused post acknowledged its records")
	}
	if !l.drained(c) {
		t.Fatalf("%d records were never posted", c.PendingCount())
	}
}
