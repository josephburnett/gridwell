package retry

import (
	"testing"
	"time"
)

func TestBackoffDoublesUpToItsCeiling(t *testing.T) {
	for _, c := range []struct {
		name string
		b    Backoff
		want []time.Duration
	}{{
		// The boot handshake's own values: what a node that answers on the
		// seventh try made the page wait.
		name: "handshake",
		b:    Backoff{First: HandshakeFirst, Max: HandshakeMax},
		want: []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second},
	}, {
		name: "ceiling below the first wait clamps it",
		b:    Backoff{First: 4 * time.Second, Max: time.Second},
		want: []time.Duration{time.Second, time.Second},
	}, {
		name: "an exact ceiling is landed on, not passed",
		b:    Backoff{First: time.Second, Max: 4 * time.Second},
		want: []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second},
	}} {
		t.Run(c.name, func(t *testing.T) {
			b := c.b
			for i, want := range c.want {
				if got := b.Next(); got != want {
					t.Fatalf("attempt %d waited %v, want %v", i+1, got, want)
				}
			}
		})
	}
}

func TestWaitServesTheWholeInterval(t *testing.T) {
	i := NewInterval(80 * time.Millisecond)
	start := time.Now()
	i.Wait()
	if el := time.Since(start); el < 80*time.Millisecond {
		t.Fatalf("waited %v, short of the interval", el)
	}
}

func TestSetShortensAWaitAlreadyRunning(t *testing.T) {
	i := NewInterval(time.Hour)
	done := make(chan time.Time, 1)
	go func() {
		i.Wait()
		done <- time.Now()
	}()
	time.Sleep(20 * time.Millisecond)
	set := time.Now()
	i.Set(10 * time.Millisecond)
	select {
	case at := <-done:
		if el := at.Sub(set); el > 2*time.Second {
			t.Fatalf("the wait ran %v past the new interval", el)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait served out the old interval")
	}
	if got := i.Duration(); got != 10*time.Millisecond {
		t.Fatalf("interval in effect is %v", got)
	}
}

func TestSetRestartsTheWaitFromNow(t *testing.T) {
	i := NewInterval(40 * time.Millisecond)
	done := make(chan time.Time, 1)
	go func() {
		i.Wait()
		done <- time.Now()
	}()
	time.Sleep(20 * time.Millisecond)
	set := time.Now()
	i.Set(200 * time.Millisecond)
	select {
	case at := <-done:
		if el := at.Sub(set); el < 200*time.Millisecond {
			t.Fatalf("the tick landed %v after the new interval was set", el)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait never returned")
	}
}

func TestReconnectWaitsAreTheDeclaredCadence(t *testing.T) {
	for _, c := range []struct {
		name string
		wait func(*Reconnect) time.Duration
		want time.Duration
	}{
		{"a failed subscribe", (*Reconnect).SubscribeFailed, SubscribeRetry},
		{"a stream that ended", (*Reconnect).StreamEnded, StreamEndPause},
	} {
		t.Run(c.name, func(t *testing.T) {
			var r Reconnect
			// Flat, not a backoff: the tenth break waits what the first did.
			start := time.Now()
			for i := 0; i < 10; i++ {
				if got := c.wait(&r); got != c.want {
					t.Fatalf("break %d waited %v, want %v", i+1, got, c.want)
				}
			}
			time.Sleep(c.wait(&r))
			if el := time.Since(start); el < c.want {
				t.Fatalf("serving the wait took %v, short of %v", el, c.want)
			}
		})
	}
}

func TestReconnectKicksOncePerGap(t *testing.T) {
	const (
		failed    = "subscribe failed"
		ended     = "stream ended"
		subscribe = "subscribed"
	)
	for _, c := range []struct {
		name string
		seq  []string
		want []bool // one entry per "subscribed" in seq
	}{{
		name: "the first stream missed nothing",
		seq:  []string{subscribe},
		want: []bool{false},
	}, {
		name: "a failed dial is a gap",
		seq:  []string{failed, subscribe},
		want: []bool{true},
	}, {
		name: "a clean EOF is a gap too",
		seq:  []string{ended, subscribe},
		want: []bool{true},
	}, {
		name: "many failures are one gap, so one kick",
		seq:  []string{failed, failed, failed, subscribe},
		want: []bool{true},
	}, {
		// Asking twice is not a second gap.
		name: "the kick is consumed",
		seq:  []string{failed, subscribe, subscribe},
		want: []bool{true, false},
	}, {
		name: "each gap gets its own kick",
		seq:  []string{ended, subscribe, ended, subscribe},
		want: []bool{true, true},
	}} {
		t.Run(c.name, func(t *testing.T) {
			var r Reconnect
			var got []bool
			for _, step := range c.seq {
				switch step {
				case failed:
					r.SubscribeFailed()
				case ended:
					r.StreamEnded()
				case subscribe:
					got = append(got, r.Subscribed())
				}
			}
			if len(got) != len(c.want) {
				t.Fatalf("subscribed %d times, want %d", len(got), len(c.want))
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("stream %d kicked %v, want %v", i+1, got[i], c.want[i])
				}
			}
		})
	}
}
