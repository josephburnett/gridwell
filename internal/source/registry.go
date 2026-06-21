package source

import "sync"

// Registry maps a source_kind token to the Source that serves it. The
// store holds one and dispatches reconciliation / mutation on a grid's
// source_kind — replacing the old hard-wired fsReader / procReader fields
// with a single lookup. The same registry backs in-process sources and (in
// the go-plugin host) loaded plugin clients.
type Registry struct {
	mu     sync.RWMutex
	byKind map[string]Source
}

func NewRegistry() *Registry {
	return &Registry{byKind: map[string]Source{}}
}

// Register adds a source under its Info().Kind. A later Register for the
// same kind replaces the earlier one (last wins), so a plugin can override
// a built-in.
func (r *Registry) Register(s Source) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byKind[s.Info().Kind] = s
}

// Get returns the source for a kind, or false if none is registered.
func (r *Registry) Get(kind string) (Source, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byKind[kind]
	return s, ok
}

// Kinds returns the registered kinds (unordered).
func (r *Registry) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byKind))
	for k := range r.byKind {
		out = append(out, k)
	}
	return out
}
