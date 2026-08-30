// Package plugin builds the registry from server config. Every entry is a
// content plugin, spawned as a gridwell-plugin-<kind> subprocess — the
// third-party door, and the one way a plugin loads. The node constructs its
// own home and transport around this call; neither is a plugin.
//
// A loaded plugin reaches the registry as a namespace.Namespace, a Go value
// the router calls directly. The plugin.v1 subprocess underneath is one of
// the node's only two gRPC hops.
package plugin

import (
	"context"
	"fmt"
	"time"

	"github.com/josephburnett/gridwell/api/compose"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/pluginhost"
)

// LoadInto registers every content plugin of the server config in reg, keyed
// by its ID: a plugin.v1 subprocess, from a binary, fronted by the pluginhost
// adapter over the node-owned store.
// front, when non-nil, wraps each loaded namespace; the node passes the source
// cache under this plugin's policy, and it is the node, not this loader, that
// decides who is cached. nil means uncached, which is the shape tests use. The
// node registers its own home and transport around this call.
func LoadInto(reg *Registry, cfg *config.ServerConfig, st *store.Store,
	front func(namespace.Namespace) namespace.Namespace) error {
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		ns, closer, err := loadPlugin(pc, st)
		if err == nil && front != nil {
			ns = front(ns)
		}
		if err != nil {
			return fmt.Errorf("plugin %q (%s): %w", pc.Kind, pc.ID, err)
		}
		// A failing handshake stops the launch: a plugin without the config
		// it needs must not come up as an empty grid, and Info is where a
		// plugin says so, with FailedPrecondition and the reason. Nothing is
		// read from the answer here; every plugin-specific behavior rides a
		// wire declaration the router reads per request.
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

// loadPlugin materializes one plugin entry: the subprocess binary, and the
// adapter joining it with the plugin's namespace of the node's store, as an
// ordinary namespace.Namespace the router calls in-process.
func loadPlugin(pc *config.PluginConfig, st *store.Store) (namespace.Namespace, func(), error) {
	// The plugin's config: its own keys plus identity. A plugin is
	// stateless by contract; the node's store is never the guest's.
	cfg := make(map[string]string, len(pc.Config)+2)
	for k, v := range pc.Config {
		cfg[k] = v
	}
	cfg["uuid"] = pc.ID
	cfg["kind"] = pc.Kind

	if pc.Binary == "" {
		return nil, nil, fmt.Errorf("kind %q: no binary path", pc.Kind)
	}
	cp, cpClose, err := compose.LoadPlugin(pc.Binary, cfg)
	if err != nil {
		return nil, nil, err
	}

	return pluginhost.New(cp, st.Namespace(pc.ID)), cpClose, nil
}
