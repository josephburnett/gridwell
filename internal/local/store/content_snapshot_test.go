package store

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// writeWhileHeld is a read context that, when the sqlite driver first consults
// it (the read holds the store's one connection), starts write and returns once
// write is queued for that connection, so the write lands after the read's
// first statement and before its next acquire, deterministically.
type writeWhileHeld struct {
	context.Context
	s     *Store
	write func()
	once  sync.Once
	done  chan struct{}
}

func (c *writeWhileHeld) Done() <-chan struct{} {
	if calledFromDriver() {
		c.once.Do(func() {
			waits := c.s.db.Stats().WaitCount
			go func() { c.write(); close(c.done) }()
			for {
				select {
				case <-c.done:
					return
				default:
				}
				if c.s.db.Stats().WaitCount > waits {
					return
				}
				runtime.Gosched()
			}
		})
	}
	return c.Context.Done()
}

func calledFromDriver() bool {
	pc := make([]uintptr, 32)
	frames := runtime.CallersFrames(pc[:runtime.Callers(2, pc)])
	for {
		f, more := frames.Next()
		if strings.HasPrefix(f.Function, "modernc.org/sqlite.") {
			return true
		}
		if !more {
			return false
		}
	}
}

// A tile that is visible is readable: a ReadContent that overlaps a content
// write answers the bytes before or the bytes after, never NotFound. The
// trace that found it: a cross-node clone creates an empty text tile, its
// event reaches a view, the view reads, and the copy's WriteContent lands
// between the read's row and its blob, so the read fetches the empty blob the
// write just released.
func TestReadContentOverlappingWriteReadsOneSnapshot(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	tile := placeText(t, s, root, 0, 0)

	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	var werr error
	ctx := &writeWhileHeld{Context: base, s: s, done: make(chan struct{}), write: func() {
		// WriteContent's text arm, the one transaction, so the write is one
		// acquire of the connection.
		_, werr = s.writeTextContent(context.Background(), tile.Id, tile.Version, []byte("copied bytes"))
	}}

	data, _, _, err := s.ReadContent(ctx, tile.Id)
	<-ctx.done
	if werr != nil {
		t.Fatalf("write: %v", werr)
	}
	if err != nil {
		t.Fatalf("read overlapping a content write: %v (the row and its blob were read from two states)", err)
	}
	if got := string(data); got != "body" && got != "copied bytes" {
		t.Fatalf("read = %q, want the bytes before or after the write", got)
	}
}
