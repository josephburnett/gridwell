// Package plugin — loader builds the registry from server config. Every
// entry is a CONTENT PLUGIN: spawned as a gridwell-plugin-<kind>
// subprocess (the third-party door) or compiled in through a Factory (the
// mobile bind — iOS forbids fork/exec). The node constructs its own home
// and transport around this call (internal/node); neither is a plugin.
//
// A loaded plugin reaches the registry as a namespace.Namespace — a Go
// value the router calls directly. The plugin.v1 subprocess underneath is
// one of the node's only two gRPC hops (docs/simplify-plan.md S2).
package plugin

import (
	"context"
	"fmt"
	"time"

	"github.com/josephburnett/gridwell/api/compose"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/pluginhost"
)

// Factory constructs an in-process plugin from the
// shared config vocabulary.
type Factory func(cfg map[string]string) (pluginv1.PluginServer, error)

// LoadInto registers every content plugin of the server config in reg,
// keyed by its ID: a plugin.v1 subprocess (binary) or a factories
// constructor, fronted by the pluginhost adapter over the NODE-owned
// memory DB. front, when non-nil, wraps each loaded namespace — the node
// passes the source cache under this plugin's policy, and it is the node
// (not this loader) that decides who is cached; nil means uncached, the
// shape tests use. The node registers its own home and transport around
// this call (internal/node).
func LoadInto(reg *Registry, cfg *config.ServerConfig, factories map[string]Factory, st *store.Store,
	front func(namespace.Namespace) namespace.Namespace) error {
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		ns, closer, err := loadPlugin(pc, factories, st)
		if err == nil && front != nil {
			ns = front(ns)
		}
		if err != nil {
			return fmt.Errorf("plugin %q (%s): %w", pc.Kind, pc.ID, err)
		}
		// A handshake that FAILS stops the launch (owner decision
		// 2026-08-27: a plugin without the config it needs must not come up
		// as an empty grid — Info is where a plugin says so,
		// FailedPrecondition with the reason). Nothing is READ from the
		// answer here: every plugin-specific behavior rides a wire
		// declaration the router reads per request (charter, 2026-08-15).
		ictx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, ierr := ns.Info(ictx, &gridwellv1.InfoRequest{})
		cancel()
		if ierr != nil {
			if closer != nil {
				closer()
			}
			return fmt.Errorf("plugin %q (%s): %w", pc.Kind, pc.ID, ierr)
		}
		reg.Register(pc.ID, pc.Kind, ns, closer)
		reg.SetLabel(pc.ID, pc.Label)
	}
	return nil
}

// loadPlugin materializes one plugin entry: the content process
// (subprocess binary or in-process factory) and the adapter joining it
// with the plugin's namespace of the node's store — an ordinary
// namespace.Namespace the router calls in-process.
func loadPlugin(pc *config.PluginConfig, pluginFactories map[string]Factory, st *store.Store) (namespace.Namespace, func(), error) {
	// The plugin's config: its own keys plus identity. A plugin is
	// stateless by contract; the node's store is never the guest's.
	cfg := make(map[string]string, len(pc.Config)+2)
	for k, v := range pc.Config {
		cfg[k] = v
	}
	cfg["uuid"] = pc.ID
	cfg["kind"] = pc.Kind

	var cp pluginv1.PluginClient
	var cpClose func()
	var err error
	if pc.Binary != "" {
		cp, cpClose, err = compose.LoadPlugin(pc.Binary, cfg)
	} else if factory, ok := pluginFactories[pc.Kind]; ok {
		impl, ferr := factory(cfg)
		if ferr != nil {
			return nil, nil, ferr
		}
		cp, cpClose, err = compose.PluginInProcess(impl)
	} else {
		return nil, nil, fmt.Errorf("kind %q: no plugin factory and no binary path", pc.Kind)
	}
	if err != nil {
		return nil, nil, err
	}

	return pluginhost.New(cp, st.Namespace(pc.ID)), cpClose, nil
}
