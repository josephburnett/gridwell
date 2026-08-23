package node

// The builtin transport (docs/v2-design.md §4.4, the v2 fold): the
// remote/ssh engine is NODE code — transports are the system's own
// topology, not a door for strangers (content-presentation.md §9).
// Remote nodes still PRESENT as plugins (the launcher tile, the menu
// row); the config entry keeps its uuid — every chained reference is
// qualified by it.

import (
	"fmt"
	"os"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/pluginmeta"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/remote"
	"github.com/josephburnett/gridwell/internal/remote/dial"
)

// NativeRemoteFactory constructs the builtin transport over its
// connection DB — the wiring the deleted gridwell-plugin-remote main
// did: identity verification, the connection store, the real dialer.
func NativeRemoteFactory(cfg map[string]string) (gridwellv1.GridwellServer, error) {
	if cfg["host"] != "" {
		return nil, fmt.Errorf("native remote: connection config keys are retired (#251) — connections are data; `gridwell serve` migrates old entries at boot")
	}
	dbPath := cfg["db_file"]
	if dbPath == "" {
		return nil, fmt.Errorf("native remote: db_file required (connections persist there)")
	}
	if _, err := pluginmeta.Verify(dbPath, cfg["uuid"], cfg["kind"]); err != nil {
		return nil, err
	}
	db, err := remote.OpenDB(dbPath)
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	return remote.New(db, dial.Dial, home), nil
}

// WithNativeTransports fills the node-native factory slots ("local",
// "remote") unless the composer supplied its own (mobile wires both for
// iOS).
func WithNativeTransports(factories map[string]plugin.ServerFactory) map[string]plugin.ServerFactory {
	factories = WithNativeLocal(factories)
	if _, ok := factories["remote"]; !ok {
		factories["remote"] = NativeRemoteFactory
	}
	return factories
}
