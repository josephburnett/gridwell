// Package plugin — loader builds the registry from server config. Every
// entry is one of two things: a NATIVE kind (local, remote — node code,
// constructed by the factories the serve wiring supplies) or a CONTENT
// PLUGIN (everything else — spawned as a gridwell-plugin-<kind>
// subprocess, the third-party door, or compiled in through a
// Factory: gridwell-all, mobile — iOS forbids fork/exec). The
// gridwell.v1 subprocess door retired 2026-08-27; plugins are plugins.
package plugin

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/josephburnett/gridwell/api/compose"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/layout"
	"github.com/josephburnett/gridwell/internal/plugin/mountcache"
	"github.com/josephburnett/gridwell/internal/pluginhost"
)

// Factory constructs an in-process plugin from the
// shared config vocabulary (the plugin twin of NativeFactory).
type Factory func(cfg map[string]string) (pluginv1.PluginServer, error)

// LoadInto registers every content plugin of the server config in reg,
// keyed by its ID: a plugin.v1 subprocess (binary) or a factories
// constructor, fronted by the pluginhost adapter over the NODE-owned
// memory DB at the entry's derived db path. The node registers its own
// home and transport around this call (internal/node).
func LoadInto(reg *Registry, cfg *config.ServerConfig, factories map[string]Factory) error {
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		client, closer, err := loadPlugin(pc, factories)
		if err != nil {
			return fmt.Errorf("plugin %q (%s): %w", pc.Kind, pc.ID, err)
		}
		// The spawn-time handshake reads the plugin's DECLARATIONS — the
		// host never derives behavior from the kind string (charter,
		// 2026-08-15). Transit-ness is the one declaration the host needs
		// synchronously (routing reads it per request), so it is cached
		// here, like identity. A handshake that FAILS stops the launch
		// (owner decision 2026-08-27: a plugin without the config it needs
		// must not come up as an empty grid — Info is where a plugin says
		// so, FailedPrecondition with the reason). The native remote
		// answers its own Info even when every connection is dark.
		ictx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		info, ierr := client.Info(ictx, &gridwellv1.InfoRequest{})
		cancel()
		if ierr != nil {
			if closer != nil {
				closer()
			}
			return fmt.Errorf("plugin %q (%s): %w", pc.Kind, pc.ID, ierr)
		}
		transit := info.GetTransit()
		// A MOUNT gets the read-through cache in front of it (mountcache,
		// offline-plan phase 1): the remote going dark degrades to
		// stale-but-readable instead of blank. A cache that cannot open
		// degrades to the uncached client — loudly, never fatally: the
		// cache is an availability layer, and refusing to serve because
		// the OPTIMIZATION broke would invert its purpose.
		if transit && cfg.CacheDir != "" {
			if mkErr := os.MkdirAll(cfg.CacheDir, 0o700); mkErr != nil {
				log.Printf("gridwell: mount cache dir %s: %v (plugin %q runs uncached)", cfg.CacheDir, mkErr, pc.ID)
			} else if cached, cacheClose, cErr := mountcache.Open(client, filepath.Join(cfg.CacheDir, pc.ID+".db")); cErr != nil {
				log.Printf("gridwell: mount cache for %q: %v (plugin runs uncached)", pc.ID, cErr)
			} else {
				client = cached
				inner := closer
				closer = func() {
					cacheClose()
					if inner != nil {
						inner()
					}
				}
			}
		}
		reg.Register(pc.ID, pc.Kind, client, closer)
		reg.SetLabel(pc.ID, pc.Label)
		reg.SetTransit(pc.ID, transit)
	}
	return nil
}

// NativeFactory is compose.NativeFactory: an in-process constructor for a
// NATIVE kind over the ONE config vocabulary plugins share.
type NativeFactory = compose.NativeFactory

// ServeInProcess is compose.ServeInProcess — re-exported for the many
// seam tests that stand a real gridwell.v1 server up in-process.
var ServeInProcess = compose.ServeInProcess

// loadPlugin materializes one plugin entry: the content process
// (subprocess binary or in-process factory), the node-owned memory DB at
// the entry's derived db path, and the adapter that joins them, served
// back as an ordinary GridwellClient.
func loadPlugin(pc *config.PluginConfig, pluginFactories map[string]Factory) (gridwellv1.GridwellClient, func(), error) {
	// The plugin's config: its own keys plus identity — but NOT
	// db_file: a plugin is stateless by contract, and the derived db
	// path is the NODE's memory DB, not the guest's to open.
	cfg := make(map[string]string, len(pc.Config)+2)
	for k, v := range pc.Config {
		cfg[k] = v
	}
	memPath := cfg["db_file"]
	delete(cfg, "db_file")
	cfg["uuid"] = pc.ID
	cfg["kind"] = pc.Kind
	if memPath == "" {
		return nil, nil, fmt.Errorf("plugin %q: no derived db path (BuildConfig injects db_file)", pc.ID)
	}

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

	mem, err := layout.OpenVerified(memPath, pc.ID, pc.Kind)
	if err != nil {
		cpClose()
		return nil, nil, err
	}
	client, adapterClose, err := compose.ServeInProcess(pluginhost.New(cp, mem))
	if err != nil {
		mem.Close()
		cpClose()
		return nil, nil, err
	}
	closer := func() {
		adapterClose()
		_ = mem.Close()
		cpClose()
	}
	return client, closer, nil
}
