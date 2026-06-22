package plugin

import (
	"fmt"
	"sync"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Registry maps plugin UUID strings to their gRPC clients. Thread-safe.
// Close() terminates all managed subprocesses; in-process plugins have no
// subprocess to terminate.
type Registry struct {
	mu      sync.RWMutex
	clients map[string]gridwellv1.GridwellClient
	// closers holds the cleanup function for each managed (subprocess) plugin.
	closers map[string]func()
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		clients: make(map[string]gridwellv1.GridwellClient),
		closers: make(map[string]func()),
	}
}

// Register adds a plugin client for the given UUID. closer, if non-nil, is
// called on Close() to terminate the backing subprocess.
func (r *Registry) Register(id string, client gridwellv1.GridwellClient, closer func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clients[id] = client
	if closer != nil {
		r.closers[id] = closer
	}
}

// Deregister removes a plugin from the registry and calls its closer. Silently
// does nothing if id is not registered.
func (r *Registry) Deregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.closers[id]; ok {
		c()
		delete(r.closers, id)
	}
	delete(r.clients, id)
}

// Get returns the client for id, or (nil, false) if not registered.
func (r *Registry) Get(id string) (gridwellv1.GridwellClient, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[id]
	return c, ok
}

// MustGet returns the client for id or panics. Useful in code paths where the
// plugin id is known to be registered.
func (r *Registry) MustGet(id string) gridwellv1.GridwellClient {
	c, ok := r.Get(id)
	if !ok {
		panic(fmt.Sprintf("plugin %q not registered", id))
	}
	return c
}

// IDs returns all registered plugin UUIDs in unspecified order.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.clients))
	for id := range r.clients {
		ids = append(ids, id)
	}
	return ids
}

// Close terminates all subprocess plugins and clears the registry.
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.closers {
		c()
		delete(r.closers, id)
	}
	r.clients = make(map[string]gridwellv1.GridwellClient)
}
