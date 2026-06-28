// Package plugin — loader builds the plugin registry from server config.
// In production every plugin (localdb, fs, proc, ssh) is a separately-compiled
// go-plugin binary, spawned because its config sets binary != "". The
// in-process factory path (loopback TCP gRPC, no subprocess) is a TEST-ONLY
// fallback used when no binary is configured — see loadOne.
package plugin

import (
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
)

// LoadAll constructs a Registry from the server config. Each PluginConfig
// entry becomes one Registry entry keyed by its ID (UUID). A plugin with a
// binary path is spawned as a subprocess via go-plugin (the production path);
// the in-process factory path is the test-only fallback.
//
// factories maps plugin kind strings to constructors used ONLY on the in-process
// (test) path. For kinds not in factories, a binary path must be provided in
// PluginConfig.Binary.
func LoadAll(cfg *config.ServerConfig, factories map[string]ServerFactory) (*Registry, error) {
	reg := NewRegistry()
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		client, closer, err := loadOne(pc, factories)
		if err != nil {
			reg.Close()
			return nil, fmt.Errorf("plugin %q (%s): %w", pc.Name, pc.ID, err)
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

	// In-process factory: a test-only path (production always sets Binary).
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
// the connection. Test-only: production plugins are subprocesses (see loadOne).
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
