package connection

// The learn bound, from both sides: a far node that accepts the dial and then
// never answers Info fails the learn when learnRootWait elapses and not
// before, and one that answers inside it lands. Both waits are derived from
// the var, which the tests shorten, so the property is the real one.

import (
	"context"
	"strings"
	"testing"
	"time"
)

// shortLearnRootWait shrinks the learn bound for one test and restores it.
func shortLearnRootWait(t *testing.T) time.Duration {
	t.Helper()
	was := learnRootWait
	t.Cleanup(func() { learnRootWait = was })
	learnRootWait = 200 * time.Millisecond
	return learnRootWait
}

// A far node whose Info hangs costs the learn exactly learnRootWait: the
// transport gives up at the bound, never earlier, and the row says why.
func TestLearnRootGivesUpAtItsBound(t *testing.T) {
	wait := shortLearnRootWait(t)
	s := gatedTransport(t, make(chan struct{})) // a gate that never opens

	type outcome struct {
		took time.Duration
		err  error
	}
	done := make(chan outcome, 1)
	start := time.Now()
	go func() {
		_, err := s.learnRoot(s.conns["rtb"])
		done <- outcome{time.Since(start), err}
	}()
	var got outcome
	select {
	case got = <-done:
	case <-time.After(3 * wait / 2):
		t.Fatalf("learnRoot was still waiting after %v — the Info must be bounded by learnRootWait (%v)", 3*wait/2, wait)
	}
	if got.err == nil {
		t.Fatal("learnRoot succeeded against a node that never answered Info")
	}
	if got.took < wait {
		t.Fatalf("learnRoot gave up after %v, before learnRootWait (%v) — a slow far node gets its wait", got.took, wait)
	}
	rows := s.Rows(context.Background())
	if len(rows) != 1 || !strings.Contains(rows[0].StatusDetail, "deadline exceeded") {
		t.Fatalf("row = %+v, want the timeout on the connection's own row", rows)
	}
}

// The other side of the same bound: an Info that answers inside it lands the
// connection, so the wait is a ceiling on a hung node and never a delay
// imposed on a slow one.
func TestLearnRootLandsAConnectionThatAnswersInsideTheBound(t *testing.T) {
	wait := shortLearnRootWait(t)
	gate := make(chan struct{})
	s := gatedTransport(t, gate)
	go func() {
		time.Sleep(wait / 4)
		close(gate)
	}()

	root, err := s.learnRoot(s.conns["rtb"])
	if err != nil || root != "rnode1/7" {
		t.Fatalf("learnRoot = %q, %v; want the far node's landing", root, err)
	}
}
