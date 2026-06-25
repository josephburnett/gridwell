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
	// kinds maps plugin UUID → kind ("fs", "proc", "localdb", …) so callers
	// that need "the fs plugin" can resolve one by kind.
	kinds map[string]string
	// labels maps plugin UUID → the server.yaml display name. This is the
	// authoritative label shown in the + menu and stamped on a mounted well,
	// so the two always agree and never depend on a plugin-derived string.
	labels map[string]string
	// order is the registration (config) order of plugin UUIDs, so the launcher
	// can present plugins exactly as configured.
	order []string
	// closers holds the cleanup function for each managed (subprocess) plugin.
	closers map[string]func()
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		clients: make(map[string]gridwellv1.GridwellClient),
		kinds:   make(map[string]string),
		labels:  make(map[string]string),
		closers: make(map[string]func()),
	}
}

// SetLabel records the configured display name (server.yaml `name`) for a
// plugin. Optional: an unset label falls back to the plugin's own Info /
// kind in ListPlugins.
func (r *Registry) SetLabel(id, label string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.labels[id] = label
}

// Label returns the configured display name for a plugin, or "" if none was
// set.
func (r *Registry) Label(id string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.labels[id]
}

// Register adds a plugin client for the given UUID and kind. closer, if
// non-nil, is called on Close() to terminate the backing subprocess.
func (r *Registry) Register(id, kind string, client gridwellv1.GridwellClient, closer func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.clients[id]; !exists {
		r.order = append(r.order, id)
	}
	r.clients[id] = client
	r.kinds[id] = kind
	if closer != nil {
		r.closers[id] = closer
	}
}

// Ordered returns (uuid, kind) for every registered plugin in registration
// (config) order.
func (r *Registry) Ordered() []struct{ UUID, Kind string } {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]struct{ UUID, Kind string }, 0, len(r.order))
	for _, id := range r.order {
		if _, ok := r.clients[id]; ok {
			out = append(out, struct{ UUID, Kind string }{id, r.kinds[id]})
		}
	}
	return out
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
	delete(r.kinds, id)
	for i, oid := range r.order {
		if oid == id {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
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
	r.kinds = make(map[string]string)
	r.order = nil
}
