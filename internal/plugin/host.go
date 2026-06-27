package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// ConfigEnvVar is the environment variable the host uses to hand a plugin its
// config map (JSON-encoded). The guest reads it back with guest.Config. This is
// how db_file / root / pid / uuid reach a subprocess plugin — there is no
// Attach config map anymore; a plugin is configured at spawn.
const ConfigEnvVar = "GRIDWELL_PLUGIN_CONFIG"

// LoadPlugin spawns the plugin binary at binaryPath, hands it cfg via the
// environment, and performs the go-plugin handshake. It returns a client and a
// closer; call closer() on shutdown.
func LoadPlugin(binaryPath string, cfg map[string]string) (gridwellv1.GridwellClient, func(), error) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "plugin-host",
		Output: hclog.DefaultOutput,
		Level:  hclog.Error,
	})

	cmd := exec.Command(binaryPath)
	cmd.Env = os.Environ()
	if len(cfg) > 0 {
		blob, err := json.Marshal(cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("plugin %q: marshal config: %w", binaryPath, err)
		}
		cmd.Env = append(cmd.Env, ConfigEnvVar+"="+string(blob))
	}

	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: HandshakeConfig,
		Plugins:         PluginMap(nil),
		Cmd:             cmd,
		AllowedProtocols: []plugin.Protocol{
			plugin.ProtocolGRPC,
		},
		Logger: logger,
		// Copy the plugin's stderr to ours so a fatal startup message (an id/kind
		// mismatch against its DB, a failed open, schema divergence) reaches the
		// user verbatim instead of being swallowed as an opaque handshake error.
		Stderr: os.Stderr,
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
