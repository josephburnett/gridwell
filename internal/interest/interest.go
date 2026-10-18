// Package interest owns what the node's clients are showing: each session's
// view (SetView), counted while that session's event stream is open and
// listed away for AwayFor after its last stream closes, and the union of every
// counted view's shown grids, which is what the node's sources are asked to
// watch. It lives in memory only; nothing here is a node fact.
package interest

import (
	"slices"
	"sort"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// maxIdle bounds the sessions kept with no stream open. A view is sent and a
// stream opened as two calls, and the node sees them in either order, so a
// view whose stream is not open is kept uncounted rather than dropped. A
// client that went away never comes back for its view, so only the newest few
// are kept.
const maxIdle = 16

// Book is the node's one record of what its clients show.
type Book struct {
	mu       sync.Mutex
	sessions map[string]*session
	idle     []string // sessions with no stream open, oldest first
	union    []string
	spread   func(union []string)
	now      func() time.Time
}

type session struct {
	streams int
	view    *pb.SetViewRequest
	changed time.Time
	away    time.Time // when its last stream closed; zero while one is open or before any
}

// Session is one session's view as the node holds it: read-only, and its
// label is View.Label.
type Session struct {
	ID      string
	View    *pb.SetViewRequest
	Changed time.Time // when the view last differed from the one before
	Open    bool      // a stream carrying the session is open
	Away    time.Time // when its last stream closed; zero while Open
}

// AwayFor is how long a session whose last stream closed is still listed, as
// a phone's browser closes its stream whenever it goes to the background.
// Away, it watches nothing: only an open stream counts toward the union.
const AwayFor = 10 * time.Minute

// New builds an empty book. spread hears each new union, in order, under the
// book's lock, so it must not block.
func New(spread func(union []string)) *Book {
	return &Book{sessions: map[string]*session{}, spread: spread, now: time.Now}
}

// Open counts one event stream under id until close. A session counts only
// while one of its streams is open, so a client that went away cannot keep a
// source watched.
func (b *Book) Open(id string) (close func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.at(id)
	s.streams++
	s.away = time.Time{}
	b.idle = slices.DeleteFunc(b.idle, func(x string) bool { return x == id })
	b.recount()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if s.streams--; s.streams == 0 {
				s.away = b.now()
				b.rest(id)
			}
			b.recount()
		})
	}
}

// Set replaces id's view. Saying the same view again, as a client does after
// every re-open, is not a change.
func (b *Book) Set(id string, v *pb.SetViewRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.at(id)
	if s.view == nil || !proto.Equal(s.view, v) {
		s.view = proto.CloneOf(v)
		s.changed = b.now()
	}
	b.recount()
}

// Union is every grid a counted session shows.
func (b *Book) Union() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.union)
}

// Sessions is every session that has said what it shows, open or away for no
// longer than AwayFor, by id.
func (b *Book) Sessions() []Session {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	var out []Session
	for id, s := range b.sessions {
		open := s.streams > 0
		if s.view == nil || !open && (s.away.IsZero() || now.Sub(s.away) > AwayFor) {
			continue
		}
		out = append(out, Session{ID: id, View: proto.CloneOf(s.view), Changed: s.changed, Open: open, Away: s.away})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (b *Book) at(id string) *session {
	s, ok := b.sessions[id]
	if !ok {
		s = &session{}
		b.sessions[id] = s
		b.rest(id)
	}
	return s
}

// rest records id as idle, forgetting the oldest idle session past maxIdle.
func (b *Book) rest(id string) {
	b.idle = append(b.idle, id)
	if len(b.idle) > maxIdle {
		delete(b.sessions, b.idle[0])
		b.idle = b.idle[1:]
	}
}

func (b *Book) recount() {
	var u []string
	for _, s := range b.sessions {
		if s.streams > 0 {
			u = append(u, s.view.GetShownGridIds()...)
		}
	}
	slices.Sort(u)
	u = slices.Compact(u)
	if slices.Equal(u, b.union) {
		return
	}
	b.union = u
	if b.spread != nil {
		b.spread(slices.Clone(u))
	}
}
