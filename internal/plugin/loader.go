// Package plugin — loader builds the plugin registry from server config.
// On desktop/server, every plugin (localdb, fs, proc, ssh) is a
// separately-compiled go-plugin binary, spawned because its config sets
// binary != "". The in-process factory path (loopback TCP gRPC, no
// subprocess) serves two callers: tests, and the MOBILE node (mobile/ —
// owner decision, offline-plan phase 2: iOS forbids fork/exec, so the
// same gRPC surface runs in-process there) — see loadOne.
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
		// The spawn-time handshake reads the plugin's DECLARATIONS — the
		// host never derives behavior from the kind string (charter,
		// 2026-08-15). Transit-ness is the one declaration the host needs
		// synchronously (routing reads it per request), so it is cached
		// here, like identity. The local binary answers even when its
		// remote is dark; a failed handshake defaults to leaf and logs —
		// a transport plugin that cannot answer its own Info is broken,
		// but a broken plugin must not stop the node.
		transit := false
		ictx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if info, ierr := client.Info(ictx, &gridwellv1.InfoRequest{}); ierr == nil {
			transit = info.GetTransit()
		} else {
			log.Printf("gridwell: plugin %q (%s): spawn handshake failed: %v (treated as a leaf plugin)", pc.Name, pc.ID, ierr)
		}
		cancel()
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

// ServerFactory is compose.Factory: an in-process plugin constructor over
// the ONE config vocabulary both process shapes share.
type ServerFactory = compose.Factory

// ServeInProcess is compose.ServeInProcess — re-exported for the many
// seam tests that stand a real plugin up without a subprocess.
var ServeInProcess = compose.ServeInProcess

func loadOne(pc *config.PluginConfig, factories map[string]ServerFactory) (gridwellv1.GridwellClient, func(), error) {
	// The one config vocabulary: the plugin's own keys plus the injected
	// identity it persists and the server verifies against its DB (see
	// pluginmeta) — uuid is the durable routing id, kind selects the
	// schema; db_file is derived upstream. Identical for a subprocess (the
	// spawn env) and an in-process factory (the argument).
	cfg := make(map[string]string, len(pc.Config)+2)
	for k, v := range pc.Config {
		cfg[k] = v
	}
	cfg["uuid"] = pc.ID
	cfg["kind"] = pc.Kind

	// The composition door (api/compose): Command for a subprocess binary
	// (desktop/server production), InProcess for a factory (tests, the
	// mobile node). Callers cannot tell which they got — that is the door's
	// contract, and the parity gate pins it.
	if pc.Binary != "" {
		return compose.Command(pc.Binary).Open(cfg)
	}
	factory, ok := factories[pc.Kind]
	if !ok {
		return nil, nil, fmt.Errorf("no factory for kind %q and no binary path", pc.Kind)
	}
	return compose.InProcess(factory).Open(cfg)
}
