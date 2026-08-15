// Package plugin — loader builds the plugin registry from server config.
// On desktop/server, every plugin (localdb, fs, proc, ssh) is a
// separately-compiled go-plugin binary, spawned because its config sets
// binary != "". The in-process factory path (loopback TCP gRPC, no
// subprocess) serves two callers: tests, and the MOBILE node (mobile/ —
// owner decision, offline-plan phase 2: iOS forbids fork/exec, so the
// same gRPC surface runs in-process there) — see loadOne.
package plugin

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin/mountcache"
)

// LoadAll constructs a Registry from the server config. Each PluginConfig
// entry becomes one Registry entry keyed by its ID (UUID). A plugin with a
// binary path is spawned as a subprocess via go-plugin (the desktop/server
// path); a plugin without one constructs in-process from factories (tests,
// and the mobile node — no fork/exec on iOS).
//
// factories maps plugin kind strings to constructors used only on the
// in-process path. For kinds not in factories, a binary path must be
// provided in PluginConfig.Binary.
func LoadAll(cfg *config.ServerConfig, factories map[string]ServerFactory) (*Registry, error) {
	reg := NewRegistry()
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		client, closer, err := loadOne(pc, factories)
		if err != nil {
			reg.Close()
			return nil, fmt.Errorf("plugin %q (%s): %w", pc.Name, pc.ID, err)
		}
		// A MOUNT gets the read-through cache in front of it (mountcache,
		// offline-plan phase 1): the remote going dark degrades to
		// stale-but-readable instead of blank. A cache that cannot open
		// degrades to the uncached client — loudly, never fatally: the
		// cache is an availability layer, and refusing to serve because
		// the OPTIMIZATION broke would invert its purpose.
		if TransitKind(pc.Kind) && cfg.CacheDir != "" {
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
	}
	return reg, nil
}

// ServerFactory is a constructor called for built-in plugin kinds. It receives
// the plugin config and returns an implementation ready to serve.
type ServerFactory func(cfg *config.PluginConfig) (gridwellv1.GridwellServer, error)

func loadOne(pc *config.PluginConfig, factories map[string]ServerFactory) (gridwellv1.GridwellClient, func(), error) {
	// Subprocess binary (the production path): spawn it via go-plugin, handing
	// it its config — including the uuid, so the plugin persists its own durable
	// identity (see pluginmeta).
	if pc.Binary != "" {
		cfg := make(map[string]string, len(pc.Config)+2)
		for k, v := range pc.Config {
			cfg[k] = v
		}
		// Inject the identity the plugin persists and the server verifies against
		// its DB (see pluginmeta): uuid is the durable routing id, kind selects
		// the schema. Both are config-authoritative; db_file is derived upstream.
		cfg["uuid"] = pc.ID
		cfg["kind"] = pc.Kind
		return LoadPlugin(pc.Binary, cfg)
	}

	// In-process factory: tests and the mobile node (desktop/server always
	// sets Binary).
	factory, ok := factories[pc.Kind]
	if !ok {
		return nil, nil, fmt.Errorf("no factory for kind %q and no binary path", pc.Kind)
	}
	impl, err := factory(pc)
	if err != nil {
		return nil, nil, fmt.Errorf("factory %q: %w", pc.Kind, err)
	}
	return ServeInProcess(impl)
}

// ServeInProcess starts a gRPC server in a goroutine on a loopback TCP port
// and returns a client connected to it. closer stops the server and closes
// the connection. Used by tests and the mobile node (desktop/server plugins
// are subprocesses — see loadOne).
func ServeInProcess(impl gridwellv1.GridwellServer) (gridwellv1.GridwellClient, func(), error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("in-process listen: %w", err)
	}

	srv := grpc.NewServer()
	gridwellv1.RegisterGridwellServer(srv, impl)
	go srv.Serve(lis)

	addr := lis.Addr().String()
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		return nil, nil, fmt.Errorf("in-process dial %s: %w", addr, err)
	}

	closer := func() {
		cc.Close()
		srv.GracefulStop()
	}
	return gridwellv1.NewGridwellClient(cc), closer, nil
}
