package plugin

import (
	"sync"

	"github.com/josephburnett/gridwell/internal/namespace"
)

// Registry maps plugin UUIDs to namespaces the router calls directly. It is
// thread-safe, and Close terminates every managed subprocess.
type Registry struct {
	mu      sync.RWMutex
	clients map[string]namespace.Namespace
	// kinds is for Ordered's listing. There is deliberately no by-kind
	// lookup.
	kinds map[string]string
	// labels are the server.yaml display names, shown in the + menu and
	// stamped on a mounted well, so neither depends on a plugin-derived
	// string.
	labels map[string]string
	// order is config order, so the + menu presents plugins as configured.
	order []string
	// closers hold each managed subprocess.
	closers map[string]func()
	// transport is the node's connection namespace, "<id>/<conn>/…". It is
	// not a plugin: the node's id qualifies it, so it has no uuid of its own
	// and never lists in Ordered. What it declares about its connections is
	// its own Handshake's answer, asked like any other namespace's.
	transport      namespace.Namespace
	transportClose func()
}

func NewRegistry() *Registry {
	return &Registry{
		clients: make(map[string]namespace.Namespace),
		kinds:   make(map[string]string),
		labels:  make(map[string]string),
		closers: make(map[string]func()),
	}
}

// SetLabel is optional: an unset label falls back to the plugin's own Info or
// kind in Handshake.
func (r *Registry) SetLabel(id, label string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.labels[id] = label
}

func (r *Registry) Label(id string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.labels[id]
}

// Register's closer, when non-nil, terminates the backing subprocess on Close.
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

// Ordered lists every registered plugin in config order.
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

// SetTransport installs the connection namespace and the closer Close runs.
func (r *Registry) SetTransport(ns namespace.Namespace, closer func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transport, r.transportClose = ns, closer
}

func (r *Registry) Transport() (namespace.Namespace, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.transport, r.transport != nil
}

func (r *Registry) Get(id string) (namespace.Namespace, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[id]
	return c, ok
}

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
	r.transport, r.transportClose = nil, nil
	r.clients = make(map[string]namespace.Namespace)
	r.kinds = make(map[string]string)
	r.labels = make(map[string]string)
	r.order = nil
}
