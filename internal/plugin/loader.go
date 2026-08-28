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
	"io"
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

// LoadAll constructs a Registry from the server config. Each
// entry becomes one Registry entry keyed by its ID. A kind present in
// factories is NATIVE and constructs in-process; every other kind is a
// plugin: a plugin.v1 subprocess (binary) or a
// pluginFactories constructor, fronted by the pluginhost adapter over
// the NODE-owned memory DB at the entry's derived db path —
// indistinguishable from a native kind above the registry.
func LoadAll(cfg *config.ServerConfig, natives map[string]NativeFactory, factories map[string]Factory) (*Registry, error) {
	reg := NewRegistry()
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		var client gridwellv1.GridwellClient
		var closer func()
		var err error
		if factory, native := natives[pc.Kind]; native {
			client, closer, err = loadNative(pc, factory)
		} else {
			client, closer, err = loadPlugin(pc, factories)
		}
		if err != nil {
			reg.Close()
			return nil, fmt.Errorf("plugin %q (%s): %w", pc.Name, pc.ID, err)
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
			reg.Close()
			return nil, fmt.Errorf("plugin %q (%s): %w", pc.Name, pc.ID, ierr)
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
				log.Printf("gridwell: mount cache dir %s: %v (mount %q runs uncached)", cfg.CacheDir, mkErr, pc.Name)
			} else if cached, cacheClose, cErr := mountcache.Open(client, filepath.Join(cfg.CacheDir, pc.ID+".db")); cErr != nil {
				log.Printf("gridwell: mount cache for %q: %v (mount runs uncached)", pc.Name, cErr)
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
		reg.SetLabel(pc.ID, pc.Name)
		reg.SetTransit(pc.ID, transit)
	}
	return reg, nil
}

// NativeFactory is compose.NativeFactory: an in-process constructor for a
// NATIVE kind over the ONE config vocabulary plugins share.
type NativeFactory = compose.NativeFactory

// ServeInProcess is compose.ServeInProcess — re-exported for the many
// seam tests that stand a real gridwell.v1 server up in-process.
var ServeInProcess = compose.ServeInProcess

// loadNative constructs a native kind: its own keys plus the injected
// identity it persists and verifies against its DB (pluginmeta) — uuid
// is the durable routing id, kind selects the schema; db_file is derived
// upstream.
func loadNative(pc *config.PluginConfig, factory NativeFactory) (gridwellv1.GridwellClient, func(), error) {
	cfg := make(map[string]string, len(pc.Config)+2)
	for k, v := range pc.Config {
		cfg[k] = v
	}
	cfg["uuid"] = pc.ID
	cfg["kind"] = pc.Kind
	impl, err := factory(cfg)
	if err != nil {
		return nil, nil, err
	}
	client, stop, err := compose.ServeInProcess(impl)
	if err != nil {
		closeImpl(pc, impl)
		return nil, nil, err
	}
	// The registry's closer owns the impl's lifecycle too, not only the
	// loopback transport: a native kind holds real resources (the local
	// store's DB, the remote's ssh sessions) and releases them in Close.
	// Transport first, so no request is in flight when the resource goes.
	return client, func() { stop(); closeImpl(pc, impl) }, nil
}

// closeImpl releases a native impl's own resources when it has any (an
// io.Closer). A close failure at shutdown is reported, never fatal — the
// process is exiting.
func closeImpl(pc *config.PluginConfig, impl gridwellv1.GridwellServer) {
	c, ok := impl.(io.Closer)
	if !ok {
		return
	}
	if err := c.Close(); err != nil {
		log.Printf("gridwell: plugin %q (%s): close: %v", pc.Name, pc.ID, err)
	}
}

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
		return nil, nil, fmt.Errorf("plugin %q: no derived db path (BuildConfig injects db_file)", pc.Name)
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
		return nil, nil, fmt.Errorf("kind %q: no plugin factory and no binary path (not a native kind either)", pc.Kind)
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
