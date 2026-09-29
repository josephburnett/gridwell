package interest

import (
	"slices"
	"testing"
)

func record() (*Book, *[][]string) {
	var heard [][]string
	b := New(func(u []string) { heard = append(heard, u) })
	return b, &heard
}

// Two clients showing overlapping grids are one union, each grid once; the
// one that disconnects takes only what nobody else shows.
func TestUnionAcrossClientsAndDisconnect(t *testing.T) {
	b, heard := record()
	closeA := b.Open("a")
	closeB := b.Open("b")
	b.Set("a", []string{"n/1", "p/2"})
	b.Set("b", []string{"p/2", "p/3"})
	if got, want := union(b), []string{"n/1", "p/2", "p/3"}; !slices.Equal(got, want) {
		t.Fatalf("union = %v, want %v", got, want)
	}
	closeB()
	if got, want := union(b), []string{"n/1", "p/2"}; !slices.Equal(got, want) {
		t.Fatalf("after b leaves, union = %v, want %v", got, want)
	}
	closeA()
	if got := union(b); len(got) != 0 {
		t.Fatalf("after both leave, union = %v, want empty", got)
	}
	if last := (*heard)[len(*heard)-1]; len(last) != 0 {
		t.Errorf("the last union spread = %v, want empty", last)
	}
}

// A set sent before its stream is open is held and counts from the open, and
// one said again while the node still holds the old stream survives that
// stream's close into the new one: the node sees a client's two calls in
// either order.
func TestSetCountsOnlyWhileAStreamIsOpen(t *testing.T) {
	b, _ := record()
	b.Set("a", []string{"p/2"})
	if got := union(b); len(got) != 0 {
		t.Fatalf("with no stream open, union = %v, want empty", got)
	}
	closeA := b.Open("a")
	if got := union(b); !slices.Equal(got, []string{"p/2"}) {
		t.Fatalf("once the stream opens, union = %v, want [p/2]", got)
	}
	b.Set("a", []string{"p/3"})
	closeA()
	if got := union(b); len(got) != 0 {
		t.Fatalf("with the stream closed, union = %v, want empty", got)
	}
	b.Open("a")
	if got := union(b); !slices.Equal(got, []string{"p/3"}) {
		t.Errorf("the re-opened stream counts %v, want [p/3]", got)
	}
}

// A client that went away is forgotten once newer idle sessions push it out,
// so the book does not grow with every page ever loaded.
func TestIdleSessionsAreBounded(t *testing.T) {
	b, _ := record()
	b.Set("gone", []string{"p/2"})
	for i := range maxIdle {
		b.Set(string(rune('a'+i)), []string{"p/9"})
	}
	b.Open("gone")
	if got := union(b); len(got) != 0 {
		t.Errorf("a session pushed out still counts %v", got)
	}
	if len(b.sessions) > maxIdle+1 {
		t.Errorf("%d sessions kept, want at most %d idle plus the open one", len(b.sessions), maxIdle)
	}
}

// A reconnect whose new stream opens before the old one is seen closing keeps
// the session's set: the session has a stream open throughout.
func TestOverlappingStreamsKeepTheSession(t *testing.T) {
	b, _ := record()
	closeOld := b.Open("a")
	b.Set("a", []string{"p/2"})
	b.Open("a")
	closeOld()
	closeOld()
	if got := union(b); !slices.Equal(got, []string{"p/2"}) {
		t.Errorf("union = %v, want [p/2] while the new stream is open", got)
	}
}

// Only a change is spread: the same union said again is silence.
func TestSpreadOnlyOnChange(t *testing.T) {
	b, heard := record()
	b.Open("a")
	b.Open("b")
	b.Set("a", []string{"p/2"})
	b.Set("b", []string{"p/2"})
	b.Set("a", []string{"p/2"})
	if len(*heard) != 1 || !slices.Equal((*heard)[0], []string{"p/2"}) {
		t.Errorf("spread %v, want exactly [[p/2]]", *heard)
	}
}

// union is the book's current union, as spread last said it.
func union(b *Book) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.union)
}
