package plugin

import (
	"fmt"
	"sort"
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
	// switches stop each source the user may switch off, keyed by the
	// namespace its health event names: a plugin's id, a connection's
	// "<node>/<name>". off is the one record of which were, held for the
	// process's life and never written anywhere.
	switches map[string]func()
	off      map[string]bool
}

func NewRegistry() *Registry {
	return &Registry{
		clients:  make(map[string]namespace.Namespace),
		kinds:    make(map[string]string),
		labels:   make(map[string]string),
		closers:  make(map[string]func()),
		switches: make(map[string]func()),
		off:      make(map[string]bool),
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

// Switch declares ns a source the user may disable, stop being what ends
// its work for the rest of the process.
func (r *Registry) Switch(ns string, stop func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.switches[ns] = stop
}

// Disable switches ns off until the process exits. A namespace with no
// switch is refused: home, and anything a far node declares, are not this
// node's to stop.
func (r *Registry) Disable(ns string) error {
	r.mu.Lock()
	stop, ok := r.switches[ns]
	already := r.off[ns]
	if ok {
		r.off[ns] = true
	}
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("%q is not a plugin or connection this node declares", ns)
	}
	if !already {
		stop()
	}
	return nil
}

// Disabled says whether the user switched ns off.
func (r *Registry) Disabled(ns string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.off[ns]
}

// DisabledNow is every source switched off, sorted.
func (r *Registry) DisabledNow() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.off))
	for ns := range r.off {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func (r *Registry) Get(id string) (namespace.Namespace, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[id]
	return c, ok
}

// Close runs every closer once the registry has let go of its lock: a closer
// takes its source's own lock, under which that source reads Disabled.
func (r *Registry) Close() {
	r.mu.Lock()
	closers, transportClose := r.closers, r.transportClose
	r.closers = make(map[string]func())
	r.transport, r.transportClose = nil, nil
	r.clients = make(map[string]namespace.Namespace)
	r.kinds = make(map[string]string)
	r.labels = make(map[string]string)
	r.switches = make(map[string]func())
	r.off = make(map[string]bool)
	r.order = nil
	r.mu.Unlock()
	for _, c := range closers {
		c()
	}
	if transportClose != nil {
		transportClose()
	}
}
