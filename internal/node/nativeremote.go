package node

// The builtin transport (docs/v2-design.md §4.4, the v2 fold): the
// remote/ssh engine is NODE code — transports are the system's own
// topology, not a door for strangers (content-presentation.md §9).
// Remote nodes still PRESENT as plugins (the launcher tile, the menu
// row); the config entry keeps its uuid — every chained reference is
// qualified by it.

import (
	"context"
	"encoding/json"
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
		return nil, fmt.Errorf("native remote: connection config keys are retired (#251) — declare connections under server.yaml `connections:`")
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
	srv := remote.New(db, dial.Dial, home)
	// CONFIG MODE (v2 #269): when server.yaml carries a `connections:`
	// key, it owns the connection set — reconcile the store against it
	// and refuse picker edits. The serve wiring passes the declarations
	// through the one flat config vocabulary as JSON.
	if raw, ok := cfg["connections_json"]; ok {
		var conns []remote.ConnSpec
		if err := json.Unmarshal([]byte(raw), &conns); err != nil {
			return nil, fmt.Errorf("native remote: connections_json: %w", err)
		}
		var retired []string
		if r := cfg["retired_json"]; r != "" {
			if err := json.Unmarshal([]byte(r), &retired); err != nil {
				return nil, fmt.Errorf("native remote: retired_json: %w", err)
			}
		}
		if _, err := remote.SyncConfig(context.Background(), db, conns, retired); err != nil {
			return nil, fmt.Errorf("native remote: connections: %w", err)
		}
		srv.SetConfigMode(true)
		// Connect NOW, before the node serves: every declared connection
		// is live or its error is in the log (Joe, 2026-08-23 — the boot
		// doesn't serve mysteries).
		srv.ConnectAll(context.Background())
	}
	return srv, nil
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
