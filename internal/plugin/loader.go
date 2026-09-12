// Package plugin builds the registry from server config. Every entry is a
// content plugin spawned as a subprocess, the one way a plugin loads; the node
// constructs its own home and transport around this call, and neither is a
// plugin. The subprocess is supervised here, so this package is the one owner
// of whether a plugin is alive.
package plugin

import (
	"context"
	"fmt"
	"os"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/pluginhost"
)

// bootInfoWait bounds the launch gate below. A var so a test can wait it out;
// see bootinfo_test.go.
var bootInfoWait = 5 * time.Second

// bootInfo is the launch gate: a plugin that cannot answer Info inside
// bootInfoWait does not come up, because a plugin without the config it needs
// must not present as an empty grid, and one that never answers must not hold
// the boot. The answer itself is discarded: the router reads declarations per
// request.
func bootInfo(ns namespace.Namespace) error {
	ctx, cancel := context.WithTimeout(context.Background(), bootInfoWait)
	defer cancel()
	_, err := ns.Info(ctx, &gridwellv1.InfoRequest{})
	return err
}

// LoadInto registers every content plugin in reg, keyed by its ID: a
// subprocess fronted by the pluginhost adapter over the node-owned store.
// Nothing is cached in front of it, a subprocess on this machine being a call
// away and the node's store the durable memory of what it minted.
func LoadInto(reg *Registry, cfg *config.ServerConfig, home string, st *store.Store) error {
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		ns, closer, err := loadPlugin(pc, home, st)
		if err != nil {
			return fmt.Errorf("plugin %q (%s): %w", pc.Kind, pc.ID, err)
		}
		if ierr := bootInfo(ns); ierr != nil {
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

// loadPlugin materializes one entry: the supervised subprocess and the adapter
// joining it with the plugin's namespace of the node's store. The client is
// built over the supervisor, so a respawn swaps the process underneath it, and
// the supervisor is also the adapter's source of health.
func loadPlugin(pc *config.PluginConfig, home string, st *store.Store) (namespace.Namespace, func(), error) {
	cfg, err := spawnConfig(pc, home)
	if err != nil {
		return nil, nil, err
	}

	if pc.Binary == "" {
		return nil, nil, fmt.Errorf("kind %q: no binary path", pc.Kind)
	}
	sup, err := Supervise(pc.ID, pc.Kind, pc.Binary, cfg)
	if err != nil {
		return nil, nil, err
	}

	return pluginhost.New(pluginv1.NewPluginClient(sup), st.Namespace(pc.ID), sup), sup.Close, nil
}

// spawnConfig carries the plugin's own keys, its identity, and state_dir, the
// private 0700 directory at <home>/plugins/<id> under cache.db's contract:
// disposable, safe to delete, rewarmed by use. Nothing deletes one, a plugin
// dropped from server.yaml being able to come back. An empty home is an error,
// so a plugin never writes into whatever directory the node started in.
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
