package namespace

// The re-dial policy is a promise about time, so the waits here are derived
// from a Backoff the test declares; one test names the real durations, which
// is where DefaultBackoff's numbers are bound.

import (
	"context"
	"errors"
	"testing"
	"time"
)

var errStreamRefused = errors.New("the namespace refused the subscription")

// recorder collects what one Refollow did: when each attempt began, and every
// transition it reported. The callbacks all run on Run's goroutine.
type recorder struct {
	started chan time.Duration
	downs   chan string
	ups     chan struct{}
	start   time.Time
}

func newRecorder() *recorder {
	return &recorder{
		started: make(chan time.Duration, 16),
		downs:   make(chan string, 16),
		ups:     make(chan struct{}, 16),
		start:   time.Now(),
	}
}

// run starts a Refollow whose attempts come from attempt, and reports when Run
// returns. The stream's context ends with the test.
func (r *recorder) run(t *testing.T, b Backoff, attempt func(ctx context.Context, established func()) error) <-chan struct{} {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() {
		defer close(done)
		Refollow{
			Label:   "test stream",
			Backoff: b,
			Down:    func(detail string) { r.downs <- detail },
			Up:      func() { r.ups <- struct{}{} },
			Attempt: func(ctx context.Context, established func()) error {
				select {
				case r.started <- time.Since(r.start):
				default:
				}
				return attempt(ctx, established)
			},
		}.Run(ctx)
	}()
	return done
}

// starts waits for n attempts and returns the gap before each one after the
// first.
func (r *recorder) gaps(t *testing.T, n int) []time.Duration {
	t.Helper()
	var at []time.Duration
	for i := 0; i < n; i++ {
		select {
		case d := <-r.started:
			at = append(at, d)
		case <-time.After(5 * time.Second):
			t.Fatalf("attempt %d never began: the loop stopped re-dialing", i+1)
		}
	}
	gaps := make([]time.Duration, 0, n-1)
	for i := 1; i < n; i++ {
		gaps = append(gaps, at[i]-at[i-1])
	}
	return gaps
}

// wantGaps fails unless each gap waited at least its declared backoff and not
// meaningfully longer.
func wantGaps(t *testing.T, got, want []time.Duration) {
	t.Helper()
	for i, w := range want {
		if got[i] < w {
			t.Errorf("re-dial %d waited %v, less than the declared %v", i+1, got[i], w)
		}
		if got[i] > w+500*time.Millisecond {
			t.Errorf("re-dial %d waited %v, want about %v", i+1, got[i], w)
		}
	}
}

// A stream that keeps failing is re-dialed on the declared schedule: First,
// doubling, capped at Max. Without this nothing holds the numbers to anything
// and a loop that hammered its namespace every millisecond would pass.
func TestRefollowWaitsTheDeclaredBackoffs(t *testing.T) {
	b := Backoff{First: 40 * time.Millisecond, Max: 160 * time.Millisecond}
	r := newRecorder()
	r.run(t, b, func(context.Context, func()) error { return errStreamRefused })

	wantGaps(t, r.gaps(t, 5), []time.Duration{40 * time.Millisecond, 80 * time.Millisecond,
		160 * time.Millisecond, 160 * time.Millisecond})
}

// An established stream is the outage ending, so the next one starts over at
// First. A backoff that only grows would leave a namespace that flaps once an
// hour re-dialing at Max forever.
func TestRefollowResetsTheBackoffWhenTheStreamEstablishes(t *testing.T) {
	b := Backoff{First: 40 * time.Millisecond, Max: 160 * time.Millisecond}
	r := newRecorder()
	attempts := 0
	r.run(t, b, func(_ context.Context, established func()) error {
		attempts++
		if attempts == 3 {
			established()
		}
		return errStreamRefused
	})

	// The third attempt establishes and then fails, so the wait before the
	// fourth is First again rather than the doubled 160ms.
	wantGaps(t, r.gaps(t, 4), []time.Duration{40 * time.Millisecond, 80 * time.Millisecond,
		40 * time.Millisecond})
}

// One outage is one down and one up, however many re-dials it takes. Reporting
// per retry would spam the client, and reporting neither would leave tiles
// quietly going stale.
func TestRefollowReportsOneDownAndOneUpPerOutage(t *testing.T) {
	r := newRecorder()
	attempts := 0
	r.run(t, Backoff{First: time.Millisecond, Max: 2 * time.Millisecond},
		func(ctx context.Context, established func()) error {
			attempts++
			if attempts <= 3 {
				return errStreamRefused
			}
			established()
			<-ctx.Done()
			return ctx.Err()
		})

	select {
	case detail := <-r.downs:
		if detail != errStreamRefused.Error() {
			t.Errorf("down detail = %q, want the stream's own failure", detail)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("three failed re-dials reported no outage")
	}
	select {
	case <-r.ups:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream came back and nothing said so")
	}
	// Whatever the loop did in between, it said it once.
	time.Sleep(50 * time.Millisecond)
	if n := len(r.downs); n != 0 {
		t.Errorf("%d extra down transitions: one outage is one report, not one per retry", n)
	}
	if n := len(r.ups); n != 0 {
		t.Errorf("%d extra up transitions", n)
	}
}

// A stream that ends without an error still ended, and the client hears why.
func TestRefollowTreatsACleanEndAsAnOutage(t *testing.T) {
	r := newRecorder()
	r.run(t, Backoff{First: time.Second, Max: time.Second},
		func(context.Context, func()) error { return nil })

	select {
	case detail := <-r.downs:
		if detail != ErrStreamEnded.Error() {
			t.Errorf("down detail = %q, want %q", detail, ErrStreamEnded.Error())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stream that ended cleanly reported nothing")
	}
}

// The numbers both fan-ins run on, bound where they are declared.
func TestDefaultBackoffIsTheDeclaredPolicy(t *testing.T) {
	if DefaultBackoff.First != time.Second {
		t.Errorf("DefaultBackoff.First = %v, want 1s", DefaultBackoff.First)
	}
	if DefaultBackoff.Max != 30*time.Second {
		t.Errorf("DefaultBackoff.Max = %v, want 30s", DefaultBackoff.Max)
	}
}
