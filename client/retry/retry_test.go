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
