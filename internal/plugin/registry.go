package plugin

import (
	"context"
	"sync"

	"github.com/josephburnett/gridwell/internal/namespace"
)

// Registry maps plugin UUID strings to their NAMESPACES — Go values the
// router calls directly (docs/simplify-plan.md S2). Thread-safe. Close()
// terminates all managed subprocesses; a namespace with no subprocess
// behind it has nothing to terminate.
type Registry struct {
	mu      sync.RWMutex
	clients map[string]namespace.Namespace
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
	// transport is the node's connection namespace ("<id>/<conn>/…"),
	// installed by the node (SetTransport). Not a plugin: it has no uuid
	// of its own — the node's id qualifies it — and it never lists in
	// Ordered.
	transport      namespace.Namespace
	transportRows  func(context.Context) []ConnectionRow
	transportClose func()
}

// ConnectionRow is one connection as the transport lists it for the
// handshake (internal/remote.Row's shape, kept here so the registry needs
// no transport import).
type ConnectionRow struct {
	Name, Label, RootGridID, StatusDetail string
	ViewCx, ViewCy, ViewZoom              float64
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		clients: make(map[string]namespace.Namespace),
		kinds:   make(map[string]string),
		labels:  make(map[string]string),
		closers: make(map[string]func()),
	}
}

// SetLabel records the configured display name (server.yaml `name`) for a
// plugin. Optional: an unset label falls back to the plugin's own Info /
// kind in Handshake.
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
func (r *Registry) Register(id, kind string, ns namespace.Namespace, closer func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.clients[id]; !exists {
		r.order = append(r.order, id)
	}
	r.clients[id] = ns
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

// SetTransport installs the node's connection namespace: its client, its
// row lister (for the handshake), and the closer Close runs.
func (r *Registry) SetTransport(ns namespace.Namespace, rows func(context.Context) []ConnectionRow, closer func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transport, r.transportRows, r.transportClose = ns, rows, closer
}

// Connections lists the transport's rows (nil without a transport).
func (r *Registry) Connections(ctx context.Context) []ConnectionRow {
	r.mu.RLock()
	rows := r.transportRows
	r.mu.RUnlock()
	if rows == nil {
		return nil
	}
	return rows(ctx)
}

// Transport returns the connection namespace, or (nil, false) when the
// node has none (unit tests over raw plugin routing).
func (r *Registry) Transport() (namespace.Namespace, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.transport, r.transport != nil
}

// Get returns the namespace for id, or (nil, false) if not registered.
func (r *Registry) Get(id string) (namespace.Namespace, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[id]
	return c, ok
}

// Close terminates all subprocess plugins and clears the registry.
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.closers {
		c()
		delete(r.closers, id)
	}
	if r.transportClose != nil {
		r.transportClose()
	}
	r.transport, r.transportRows, r.transportClose = nil, nil, nil
	r.clients = make(map[string]namespace.Namespace)
	r.kinds = make(map[string]string)
	r.labels = make(map[string]string)
	r.order = nil
}
