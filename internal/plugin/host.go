package plugin

import (
	"fmt"
	"os/exec"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// LoadPlugin spawns the plugin binary at binaryPath and performs the go-plugin
// handshake. It returns a client and a closer; call closer() on shutdown.
func LoadPlugin(binaryPath string) (gridwellv1.GridwellClient, func(), error) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "plugin-host",
		Output: hclog.DefaultOutput,
		Level:  hclog.Error,
	})

	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: HandshakeConfig,
		Plugins:         PluginMap(nil),
		Cmd:             exec.Command(binaryPath),
		AllowedProtocols: []plugin.Protocol{
			plugin.ProtocolGRPC,
		},
		Logger: logger,
	})

	rpcClient, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, nil, fmt.Errorf("plugin dial %q: %w", binaryPath, err)
	}

	raw, err := rpcClient.Dispense(PluginName)
	if err != nil {
		client.Kill()
		return nil, nil, fmt.Errorf("plugin dispense %q: %w", binaryPath, err)
	}

	grpcClient, ok := raw.(gridwellv1.GridwellClient)
	if !ok {
		client.Kill()
		return nil, nil, fmt.Errorf("plugin %q: unexpected type %T", binaryPath, raw)
	}

	return grpcClient, client.Kill, nil
}
