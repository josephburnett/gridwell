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
	"os"
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
// adapter over the node-owned store. Nothing is cached in front of it — a
// subprocess on this machine is a call away, and the node's memory of what it
// minted is the durable store. The node registers its own home and transport
// around this call.
func LoadInto(reg *Registry, cfg *config.ServerConfig, home string, st *store.Store) error {
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		ns, closer, err := loadPlugin(pc, home, st)
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
// ordinary namespace.Namespace the router calls in-process. home is the
// Gridwell home config.Home() derived, threaded from the node; the plugin's
// state directory hangs off it.
func loadPlugin(pc *config.PluginConfig, home string, st *store.Store) (namespace.Namespace, func(), error) {
	cfg, err := spawnConfig(pc, home)
	if err != nil {
		return nil, nil, err
	}

	if pc.Binary == "" {
		return nil, nil, fmt.Errorf("kind %q: no binary path", pc.Kind)
	}
	cp, cpClose, err := compose.LoadPlugin(pc.Binary, cfg)
	if err != nil {
		return nil, nil, err
	}

	return pluginhost.New(cp, st.Namespace(pc.ID)), cpClose, nil
}

// spawnConfig is the config map one plugin is spawned with: its own keys, its
// identity, and state_dir — the private directory the node mints for it at
// <home>/plugins/<id>, 0700. A plugin holds no node facts; the node's store is
// never the guest's. What it may keep in that directory is its own memory of
// its source, under cache.db's contract: disposable, safe to delete, rewarmed
// by use. Nothing here or anywhere else deletes one — a plugin dropped from
// server.yaml may come back, and things stay as the user left them.
//
// An empty home is an error, not a relative path: a plugin must never write
// into whatever directory the node happened to start in.
func spawnConfig(pc *config.PluginConfig, home string) (map[string]string, error) {
	if home == "" {
		return nil, fmt.Errorf("no home directory for the plugin's state_dir")
	}
	dir := config.PluginStateDir(home, pc.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("state dir %s: %w", dir, err)
	}
	cfg := make(map[string]string, len(pc.Config)+3)
	for k, v := range pc.Config {
		cfg[k] = v
	}
	cfg["uuid"] = pc.ID
	cfg["kind"] = pc.Kind
	cfg["state_dir"] = dir
	return cfg, nil
}
