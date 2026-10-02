package sourcecache

import "sync"

// foldOrder is the one owner of which is newer, a live answer or a fold: an
// answer the source gave before a fold the layer applied since is older than
// the rows it would replace, so it is not installed. Keys are the ids a write
// touches, grid and tile alike.
type foldOrder struct {
	mu   sync.Mutex
	seq  uint64
	last map[string]uint64 // key -> seq of the latest fold that touched it
	open map[uint64]int    // live reads in flight, by the seq each began at
}

// liveRead is one read of the source in flight, from before its call to the
// install of its answer.
type liveRead struct {
	o  *foldOrder
	at uint64
}

func (o *foldOrder) begin() liveRead {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.open == nil {
		o.open, o.last = map[uint64]int{}, map[string]uint64{}
	}
	o.open[o.seq]++
	return liveRead{o: o, at: o.seq}
}

// end retires the read. With none in flight no answer can be compared, so the
// fold record is dropped rather than grown forever.
func (r liveRead) end() {
	o := r.o
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.open[r.at]--; o.open[r.at] == 0 {
		delete(o.open, r.at)
	}
	if len(o.open) == 0 {
		clear(o.last)
	}
}

// install runs write unless a fold touched one of keys since the read began,
// and reports whether it ran. The write runs under the lock a fold's record
// takes, so a fold either lands after it or is seen by it.
func (r liveRead) install(keys []string, write func()) bool {
	o := r.o
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, k := range keys {
		if o.last[k] > r.at {
			return false
		}
	}
	write()
	return true
}

// fold records a fold over keys and then applies it. The record comes first:
// an install that checked before it has already written, so the fold lands
// last.
func (o *foldOrder) fold(keys []string, write func()) {
	o.mu.Lock()
	if len(o.open) > 0 {
		o.seq++
		for _, k := range keys {
			if k != "" {
				o.last[k] = o.seq
			}
		}
	}
	o.mu.Unlock()
	write()
}
