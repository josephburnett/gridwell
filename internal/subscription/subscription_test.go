package subscription

import (
	"slices"
	"testing"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/trace"
)

// shown is a view of grids alone, the part the union reads.
func shown(grids ...string) *pb.SetViewRequest {
	return &pb.SetViewRequest{ShownGridIds: grids}
}

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
	b.Set("a", shown("n/1", "p/2"))
	b.Set("b", shown("p/2", "p/3"))
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

// A view sent before its stream is open is held and counts from the open, and
// one said again while the node still holds the old stream survives that
// stream's close into the new one: the node sees a client's two calls in
// either order.
func TestSetCountsOnlyWhileAStreamIsOpen(t *testing.T) {
	b, _ := record()
	b.Set("a", shown("p/2"))
	if got := union(b); len(got) != 0 {
		t.Fatalf("with no stream open, union = %v, want empty", got)
	}
	closeA := b.Open("a")
	if got := union(b); !slices.Equal(got, []string{"p/2"}) {
		t.Fatalf("once the stream opens, union = %v, want [p/2]", got)
	}
	b.Set("a", shown("p/3"))
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
	b.Set("gone", shown("p/2"))
	for i := range maxIdle {
		b.Set(string(rune('a'+i)), shown("p/9"))
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
	b.Set("a", shown("p/2"))
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
	b.Set("a", shown("p/2"))
	b.Set("b", shown("p/2"))
	b.Set("a", shown("p/2"))
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

// A session's view is listed once a stream carrying it is open, with when it
// last changed: the same view said again after a re-open is no change.
func TestSessionsListOnceAStreamOpens(t *testing.T) {
	b, _ := record()
	clock := time.Unix(100, 0)
	b.now = func() time.Time { return clock }
	view := &pb.SetViewRequest{Label: "desktop", ShownGridIds: []string{"p/2"},
		Panes: []*pb.ViewPane{{Id: "p0", Anchor: "p/1", Path: []string{"p/5"}, GridId: "p/2", Focused: true}}}
	b.Set("a", view)
	if got := b.Sessions(); len(got) != 0 {
		t.Fatalf("with no stream open, sessions = %v, want none", got)
	}
	closeA := b.Open("a")
	got := b.Sessions()
	if len(got) != 1 || got[0].ID != "a" || got[0].View.GetLabel() != "desktop" ||
		got[0].View.GetPanes()[0].GetGridId() != "p/2" || !got[0].Changed.Equal(time.Unix(100, 0)) {
		t.Fatalf("sessions = %v, want a's desktop view changed at 100", got)
	}
	clock = time.Unix(200, 0)
	b.Set("a", view)
	if c := b.Sessions()[0].Changed; !c.Equal(time.Unix(100, 0)) {
		t.Errorf("the same view said again moved changed to %v", c)
	}
	b.Set("a", shown("p/3"))
	if c := b.Sessions()[0].Changed; !c.Equal(time.Unix(200, 0)) {
		t.Errorf("a new view left changed at %v, want 200", c)
	}
	closeA()
	if got := b.Sessions(); len(got) != 1 || got[0].Open {
		t.Errorf("after the stream closed, sessions = %v, want a listed away", got)
	}
}

// A session whose last stream closed is listed away, with when, for AwayFor
// and not after; a stream opened again clears it. An away session watches
// nothing.
func TestAwaySessionsListedForAwayFor(t *testing.T) {
	b, _ := record()
	clock := time.Unix(100, 0)
	b.now = func() time.Time { return clock }
	view := &pb.SetViewRequest{Label: "browser", ShownGridIds: []string{"p/2"},
		Panes: []*pb.ViewPane{{Id: "p0", Anchor: "p/1", GridId: "p/2", Focused: true}}}
	closeA := b.Open("a")
	b.Set("a", view)
	if got := b.Sessions(); len(got) != 1 || !got[0].Open || !got[0].Away.IsZero() {
		t.Fatalf("with its stream open, sessions = %v, want a open", got)
	}
	clock = time.Unix(150, 0)
	closeA()
	got := b.Sessions()
	if len(got) != 1 || got[0].ID != "a" || got[0].Open || !got[0].Away.Equal(time.Unix(150, 0)) ||
		got[0].View.GetLabel() != "browser" {
		t.Fatalf("after the stream closed, sessions = %v, want a away since 150", got)
	}
	if u := union(b); len(u) != 0 {
		t.Errorf("an away session counts %v, want nothing watched", u)
	}
	clock = time.Unix(150, 0).Add(AwayFor)
	if got := b.Sessions(); len(got) != 1 {
		t.Errorf("at the bound, sessions = %v, want a still listed", got)
	}
	clock = clock.Add(time.Second)
	if got := b.Sessions(); len(got) != 0 {
		t.Errorf("past the bound, sessions = %v, want none", got)
	}
	b.Open("a")
	if got := b.Sessions(); len(got) != 1 || !got[0].Open || !got[0].Away.IsZero() {
		t.Errorf("open again, sessions = %v, want a open with no away time", got)
	}
}

// sessionStates is every view/session record of id, in order, as state:streams.
func sessionStates(id string) []string {
	var out []string
	for _, rec := range trace.Default().Snapshot() {
		if rec.Src == "view" && rec.Kind == "session" && rec.KV["session"] == id {
			out = append(out, rec.KV["state"]+":"+rec.KV["streams"])
		}
	}
	return out
}

// A session's life is in the trace: each stream opening and closing, going
// away when its last one closes, expiring once a listing leaves it out past
// AwayFor, and being forgotten when newer idle sessions push it out.
func TestASessionsLifeIsTraced(t *testing.T) {
	b, _ := record()
	clock := time.Unix(100, 0)
	b.now = func() time.Time { return clock }
	const id = "trace-life"
	first := b.Open(id)
	second := b.Open(id)
	b.Set(id, shown("p/2"))
	second()
	first()
	clock = clock.Add(AwayFor + time.Second)
	b.Sessions()
	b.Sessions()
	for i := range maxIdle {
		b.Set("trace-push-"+string(rune('a'+i)), shown("p/9"))
	}
	want := []string{"open:1", "open:2", "open:1", "away:0", "expired:0", "forgotten:0"}
	if got := sessionStates(id); !slices.Equal(got, want) {
		t.Errorf("records = %v, want %v", got, want)
	}
}
