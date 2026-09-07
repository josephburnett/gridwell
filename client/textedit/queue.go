package textedit

import "sync"

// SaveQueue serializes content writes per document (SaveQueueKey names the
// chain): one in flight, the rest FIFO.
// Text saves are optimistic-concurrency writes — each claims the tile's
// version — so two pipelined saves would claim the same version and the
// loser's edit would be rejected and reconciled away, losing typed input.
// Serializing means each task runs after the previous write's response has
// advanced the cached version, so tasks that read the version at send time
// chain instead of race.
type SaveQueue struct {
	mu      sync.Mutex
	busy    map[string]bool
	pending map[string][]func()
}

// SaveQueueKey names the chain a text save belongs on: one document, one
// chain. A leaf link and its target are one document — one {bytes, base,
// dirty} cache entry, one version to claim — so the id that OWNS the bytes
// names the chain, never the row the edit was viewed through.
//
// The rule needs an owner because the flush paths hold different ids: the
// ascent flush holds the viewed row (for a leaf link, the link row) and the
// debounce sweep holds the content id. Spelled per call site, those are two
// chains for one document, and an ascent flush and a debounce flush of the
// same edit then run concurrently, both claim the same SaveBasis, and the
// server refuses the loser — the client conflicting with itself.
//
// An empty content id means the caller could not resolve the owner row; the
// viewed row is then the document, as rpc.Tile.ContentID and the wasm
// contentKey both fall back, and never the empty chain every document would
// share.
func SaveQueueKey(viewedID, contentID string) string {
	if contentID != "" {
		return contentID
	}
	return viewedID
}

// NewSaveQueue returns an empty queue.
func NewSaveQueue() *SaveQueue {
	return &SaveQueue{busy: map[string]bool{}, pending: map[string][]func(){}}
}

// Enqueue schedules task on key's serial chain. Tasks for one key run
// strictly one-after-another (each fully returns before the next starts);
// different keys are independent. task runs on a queue goroutine — it should
// do the blocking send itself, not spawn.
func (q *SaveQueue) Enqueue(key string, task func()) {
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

func (q *SaveQueue) run(key string, task func()) {
	for {
		task()
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
