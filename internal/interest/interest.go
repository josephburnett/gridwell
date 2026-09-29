// Package interest owns which grids the node's clients are showing: each
// session's set, counted while that session's event stream is open, and the
// union of every counted set, which is what the node's sources are asked to
// watch.
package interest

import (
	"slices"
	"sync"
)

// maxIdle bounds the sessions kept with no stream open. A set is sent and a
// stream opened as two calls, and the node sees them in either order, so a
// set whose stream is not open is kept uncounted rather than dropped. A client
// that went away never comes back for its set, so only the newest few are
// kept.
const maxIdle = 16

// Book is the node's one record of interest.
type Book struct {
	mu       sync.Mutex
	sessions map[string]*session
	idle     []string // sessions with no stream open, oldest first
	union    []string
	spread   func(union []string)
}

type session struct {
	streams int
	grids   []string
}

// New builds an empty book. spread hears each new union, in order, under the
// book's lock, so it must not block.
func New(spread func(union []string)) *Book {
	return &Book{sessions: map[string]*session{}, spread: spread}
}

// Open counts one event stream under id until close. A session counts only
// while one of its streams is open, so a client that went away cannot keep a
// source watched.
func (b *Book) Open(id string) (close func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.at(id)
	s.streams++
	b.idle = slices.DeleteFunc(b.idle, func(x string) bool { return x == id })
	b.recount()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if s.streams--; s.streams == 0 {
				b.rest(id)
			}
			b.recount()
		})
	}
}

// Set replaces id's set.
func (b *Book) Set(id string, grids []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.at(id).grids = slices.Clone(grids)
	b.recount()
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
			u = append(u, s.grids...)
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
