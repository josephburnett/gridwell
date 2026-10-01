package outbox

import "sync"

// SaveQueue serializes content writes per key: one in flight, the rest FIFO,
// so the write enqueued last is the one the node holds last. A text body's
// key is textedit.SaveQueueKey; a pane layout's is its tile id. Two in flight
// would let a text save claim a version its sibling already claimed, and an
// older layout land over the one a flush waiter was told the node holds.
type SaveQueue struct {
	mu      sync.Mutex
	busy    map[string]bool
	pending map[string][]func(prev bool) bool
}

// NewSaveQueue returns an empty queue.
func NewSaveQueue() *SaveQueue {
	return &SaveQueue{busy: map[string]bool{}, pending: map[string][]func(bool) bool{}}
}

// Enqueue schedules task on key's chain; different keys are independent.
// task runs on a queue goroutine, does the blocking send itself rather than
// spawn, and reports whether its write landed.
func (q *SaveQueue) Enqueue(key string, task func() (landed bool)) {
	q.add(key, func(bool) bool { return task() })
}

// After runs held on key's chain once every write enqueued before it has
// finished, with whether the last of them landed. An idle chain answers true:
// nothing is owed.
func (q *SaveQueue) After(key string, held func(landed bool)) {
	q.add(key, func(prev bool) bool { held(prev); return prev })
}

func (q *SaveQueue) add(key string, task func(prev bool) bool) {
	q.mu.Lock()
	if q.busy[key] {
		q.pending[key] = append(q.pending[key], task)
		q.mu.Unlock()
		return
	}
	q.busy[key] = true
	q.mu.Unlock()
	go q.run(key, task)
}

func (q *SaveQueue) run(key string, task func(bool) bool) {
	landed := true
	for {
		landed = task(landed)
		q.mu.Lock()
		next := q.pending[key]
		if len(next) == 0 {
			q.busy[key] = false
			delete(q.pending, key)
			q.mu.Unlock()
			return
		}
		task = next[0]
		q.pending[key] = next[1:]
		q.mu.Unlock()
	}
}
