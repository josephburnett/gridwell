package todos

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Source pages GitLab's todo list for one state, NEWEST FIRST (GitLab
// orders /todos by id descending, and ids rise with creation). Page
// numbers start at 1; more is false on the last page. The ordering is
// verified per page, never assumed — see Sync.
type Source interface {
	Page(ctx context.Context, state string, page int) (todos []Todo, more bool, err error)
}

// Memory is everything the plugin has seen this process lifetime, keyed by
// todo id. A todo that vanishes from GitLab keeps its record here and shows as
// done; nothing is ever removed. Durable memory is the node's, in its
// read-through cache: a plugin is stateless by contract, so a restart re-walks
// GitLab and the node bridges the gap.
type Memory struct {
	mu    sync.Mutex
	todos map[int64]*Todo
	// doneComplete records that some walk reached the end of the done list.
	// Only then does a page of already-known done todos prove every older one
	// is known: a targeted week walk stops at the week boundary having
	// absorbed one page past it, so until a walk has run to the end, a
	// fully-known page proves nothing about the rest.
	doneComplete bool
}

// NewMemory builds an empty memory.
func NewMemory() *Memory { return &Memory{todos: map[int64]*Todo{}} }

// Sync refreshes the memory from src. A zero since walks everything: every
// pending page, then done pages to the end. Once a walk has reached that end,
// later walks stop at the first page that carries nothing new, because a page
// of already-known done todos then means every older one is known too, since
// done todos only enter at their own position. A non-zero since is the
// targeted walk for one week: both states stop as soon as a page reaches todos
// created before since.
//
// Completion is derived: a remembered pending todo the pending walk did not
// see, within the walk's coverage, is done, whether it was marked done or
// deleted with its target. That derivation is the only place a todo's state
// changes without GitLab saying so, and it is only safe when the walk really
// covered the todo's creation time. A page that is not newest-first therefore
// disables the early stop, and the walk runs to the end, rather than risk
// marking live todos done.
func (m *Memory) Sync(ctx context.Context, src Source, since time.Time) error {
	seenPending := map[int64]bool{}
	// coverage is the oldest creation time the pending walk provably
	// enumerated past; zero means everything.
	coverage := since
	fullWalk := false
	for page := 1; ; page++ {
		todos, more, err := src.Page(ctx, StatePending, page)
		if err != nil {
			return err
		}
		m.absorb(todos)
		for i := range todos {
			seenPending[todos[i].ID] = true
		}
		if !more {
			fullWalk = true
			break
		}
		if since.IsZero() || !descending(todos) {
			continue
		}
		if len(todos) > 0 && todos[len(todos)-1].CreatedAt.Before(since) {
			break
		}
	}
	if fullWalk {
		coverage = time.Time{}
	}
	m.mu.Lock()
	for _, t := range m.todos {
		if t.State != StatePending || seenPending[t.ID] {
			continue
		}
		if coverage.IsZero() || !t.CreatedAt.Before(coverage) {
			t.State = StateDone
		}
	}
	m.mu.Unlock()

	for page := 1; ; page++ {
		todos, more, err := src.Page(ctx, StateDone, page)
		if err != nil {
			return err
		}
		unknown := m.absorb(todos)
		if !more {
			m.mu.Lock()
			m.doneComplete = true
			m.mu.Unlock()
			break
		}
		m.mu.Lock()
		complete := m.doneComplete
		m.mu.Unlock()
		if complete && unknown == 0 {
			break
		}
		if since.IsZero() || !descending(todos) {
			continue
		}
		if len(todos) > 0 && todos[len(todos)-1].CreatedAt.Before(since) {
			break
		}
	}
	return nil
}

// absorb records a page, with GitLab's record replacing the remembered one,
// its state included, so a restored todo goes back to pending. It returns how
// many were new.
func (m *Memory) absorb(todos []Todo) (unknown int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range todos {
		t := todos[i]
		if _, ok := m.todos[t.ID]; !ok {
			unknown++
		}
		m.todos[t.ID] = &t
	}
	return unknown
}

// descending reports whether a page is ordered newest-first.
func descending(todos []Todo) bool {
	for i := 1; i < len(todos); i++ {
		if todos[i].CreatedAt.After(todos[i-1].CreatedAt) {
			return false
		}
	}
	return true
}

// Get answers one remembered todo (a copy).
func (m *Memory) Get(id int64) (Todo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.todos[id]
	if !ok {
		return Todo{}, false
	}
	return *t, true
}

// All answers every remembered todo, oldest first (ties by id).
func (m *Memory) All() []Todo {
	m.mu.Lock()
	out := make([]Todo, 0, len(m.todos))
	for _, t := range m.todos {
		out = append(out, *t)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Week answers the remembered todos created in the week starting at
// start, oldest first.
func (m *Memory) Week(start time.Time) []Todo {
	end := start.AddDate(0, 0, 7)
	var out []Todo
	for _, t := range m.All() {
		if !t.CreatedAt.Before(start) && t.CreatedAt.Before(end) {
			out = append(out, t)
		}
	}
	return out
}

// WeekSummary is one week of the root listing.
type WeekSummary struct {
	Start      time.Time
	Open, Done int
}

// Weeks answers every week that holds a remembered todo, newest first.
func (m *Memory) Weeks() []WeekSummary {
	byStart := map[time.Time]*WeekSummary{}
	for _, t := range m.All() {
		s := WeekStart(t.CreatedAt)
		w := byStart[s]
		if w == nil {
			w = &WeekSummary{Start: s}
			byStart[s] = w
		}
		if t.Done() {
			w.Done++
		} else {
			w.Open++
		}
	}
	out := make([]WeekSummary, 0, len(byStart))
	for _, w := range byStart {
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	return out
}
