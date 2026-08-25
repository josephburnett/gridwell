package plugin

import (
	"sync"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Registry maps plugin UUID strings to their gRPC clients. Thread-safe.
// Close() terminates all managed subprocesses; in-process plugins have no
// subprocess to terminate.
type Registry struct {
	mu      sync.RWMutex
	clients map[string]gridwellv1.GridwellClient
	// kinds maps plugin UUID → kind — carried for Ordered()'s listing
	// only; there is deliberately NO by-kind lookup (the host never
	// switches on a plugin kind — charter).
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
	// transit holds each plugin's DECLARED transit-ness (SetTransit).
	transit map[string]bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		clients: make(map[string]gridwellv1.GridwellClient),
		kinds:   make(map[string]string),
		labels:  make(map[string]string),
		closers: make(map[string]func()),
		transit: make(map[string]bool),
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

// SetTransit records the plugin's DECLARED transit-ness (InfoResponse.
// transit, read once from the spawn-time handshake by the loader — the
// local transport binary is alive even when its remote isn't, so the fact
// is as stable as identity). The host never derives it from the kind
// string (charter, 2026-08-15: the host must not know its plugins).
func (r *Registry) SetTransit(id string, transit bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transit[id] = transit
}

// Transit reports whether the plugin's ids are CHAINS from another node — a
// node mount, where the plugin forwards to a remote gridwell's front door and
// its ids arrive already qualified from the remote's perspective. The server's
// qualification layer prepends this plugin's uuid to every id it returns
// (qualifyTilesTransit) instead of applying leaf-plugin rules. The fact is
// the plugin's own declaration (SetTransit), cached at spawn.
func (r *Registry) Transit(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.transit[id]
}

// Get returns the client for id, or (nil, false) if not registered.
func (r *Registry) Get(id string) (gridwellv1.GridwellClient, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[id]
	return c, ok
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
