// Package host loads an out-of-process Gridwell plugin binary via
// go-plugin and returns a gridwellv1.GridwellClient that the caller uses
// just like any other in-process implementation.
package host

import (
	"fmt"
	"os/exec"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	gplug "github.com/josephburnett/gridwell/internal/plugin"
)

// Plugin is a loaded out-of-process plugin. Call Client() to get the
// gRPC client, and Close() to terminate the subprocess on shutdown.
type Plugin struct {
	client *plugin.Client
	grpc   gridwellv1.GridwellClient
}

// Load spawns the plugin binary at binaryPath and performs the go-plugin
// handshake. The returned Plugin is ready for use; call Close() when done.
func Load(binaryPath string) (*Plugin, error) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "plugin-host",
		Output: hclog.DefaultOutput,
		Level:  hclog.Error,
	})

	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: gplug.HandshakeConfig,
		Plugins:         gplug.PluginMap(nil),
		Cmd:             exec.Command(binaryPath),
		AllowedProtocols: []plugin.Protocol{
			plugin.ProtocolGRPC,
		},
		Logger: logger,
	})

	rpcClient, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("plugin dial %q: %w", binaryPath, err)
	}

	raw, err := rpcClient.Dispense(gplug.PluginName)
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("plugin dispense %q: %w", binaryPath, err)
	}

	grpcClient, ok := raw.(gridwellv1.GridwellClient)
	if !ok {
		client.Kill()
		return nil, fmt.Errorf("plugin %q: unexpected type %T", binaryPath, raw)
	}

	return &Plugin{client: client, grpc: grpcClient}, nil
}

// Client returns the gRPC client for this plugin. The client is valid until
// Close() is called.
func (p *Plugin) Client() gridwellv1.GridwellClient { return p.grpc }

// Close terminates the plugin subprocess.
func (p *Plugin) Close() { p.client.Kill() }
