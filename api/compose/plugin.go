package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
)

// ConfigEnvVar carries the plugin's config map to the guest as JSON.
const ConfigEnvVar = "GRIDWELL_PLUGIN_CONFIG"

// HostPIDEnvVar carries the host's pid; go-plugin gives the guest no other
// host-death signal.
const HostPIDEnvVar = "GRIDWELL_HOST_PID"

// PluginName is the go-plugin dispatch key for the plugin service.
const PluginName = "gridwell-plugin"

type pluginGRPCPlugin struct {
	plugin.Plugin
	Impl pluginv1.PluginServer
}

func (p *pluginGRPCPlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	pluginv1.RegisterPluginServer(s, p.Impl)
	return nil
}

// GRPCClient hands back the connection, not a typed client: internal/plugin
// keeps one client for the plugin's life and swaps the process under it.
func (p *pluginGRPCPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return c, nil
}

// PluginMap is the go-plugin plugin map. impl is set guest-side, nil
// host-side.
func PluginMap(impl pluginv1.PluginServer) map[string]plugin.Plugin {
	return map[string]plugin.Plugin{
		PluginName: &pluginGRPCPlugin{Impl: impl},
	}
}

// Process is one running plugin subprocess.
type Process struct {
	// Conn carries every plugin.v1 call; wrap it with
	// pluginv1.NewPluginClient.
	Conn   grpc.ClientConnInterface
	client *plugin.Client
}

// ID is the subprocess pid.
func (p *Process) ID() string { return p.client.ID() }

// Exited reports whether the subprocess is gone. go-plugin offers no channel
// to select on, so callers poll.
func (p *Process) Exited() bool { return p.client.Exited() }

// Kill terminates the subprocess and waits for it. Safe to call twice.
func (p *Process) Kill() { p.client.Kill() }

// LoadPlugin spawns a plugin binary. The config map and the host pid ride
// the spawn environment. stderr takes the subprocess's own stderr and
// go-plugin's host-side errors, which reach no logger of the node's; nil is
// os.Stderr.
func LoadPlugin(binaryPath string, cfg map[string]string, stderr io.Writer) (*Process, error) {
	if stderr == nil {
		stderr = os.Stderr
	}
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "plugin-host",
		Output: stderr,
		Level:  hclog.Error,
	})

	cmd := exec.Command(binaryPath)
	cmd.Env = os.Environ()
	if len(cfg) > 0 {
		blob, err := json.Marshal(cfg)
		if err != nil {
			return nil, fmt.Errorf("plugin %q: marshal config: %w", binaryPath, err)
		}
		cmd.Env = append(cmd.Env, ConfigEnvVar+"="+string(blob))
	}
	cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%d", HostPIDEnvVar, os.Getpid()))

	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  HandshakeConfig,
		Plugins:          PluginMap(nil),
		Cmd:              cmd,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		Logger:           logger,
		Stderr:           stderr,
	})

	rpcClient, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("plugin dial %q: %w", binaryPath, err)
	}
	raw, err := rpcClient.Dispense(PluginName)
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("plugin dispense %q: %w", binaryPath, err)
	}
	conn, ok := raw.(grpc.ClientConnInterface)
	if !ok {
		client.Kill()
		return nil, fmt.Errorf("plugin %q: unexpected type %T", binaryPath, raw)
	}
	return &Process{Conn: conn, client: client}, nil
}
