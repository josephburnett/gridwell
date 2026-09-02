package compose

// The content-plugin half of compose: a plugin binary serves plugin.v1, and
// LoadPlugin is the one way a host reaches it.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
)

// ConfigEnvVar is the environment variable the host uses to hand a
// plugin its config map (JSON) at spawn — the guest helper decodes it.
const ConfigEnvVar = "GRIDWELL_PLUGIN_CONFIG"

// HostPIDEnvVar carries the spawning host's pid to the guest, which
// watches it and exits when the host dies. go-plugin gives the guest no
// host-death detection in our configuration.
const HostPIDEnvVar = "GRIDWELL_HOST_PID"

// PluginName is the go-plugin dispatch key for the plugin
// service.
const PluginName = "gridwell-plugin"

// pluginGRPCPlugin bridges go-plugin's transport and the
// Plugin service.
type pluginGRPCPlugin struct {
	plugin.Plugin
	Impl pluginv1.PluginServer
}

func (p *pluginGRPCPlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	pluginv1.RegisterPluginServer(s, p.Impl)
	return nil
}

// GRPCClient hands the host the CONNECTION, not a typed client. A supervisor
// keeps one plugin.v1 client for the life of the plugin and swaps the process
// underneath it (internal/plugin), which it can only do if it owns the
// connection the client is built over.
func (p *pluginGRPCPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return c, nil
}

// PluginMap is the plugin map for plugin binaries — impl set on
// the guest side, nil on the host side.
func PluginMap(impl pluginv1.PluginServer) map[string]plugin.Plugin {
	return map[string]plugin.Plugin{
		PluginName: &pluginGRPCPlugin{Impl: impl},
	}
}

// Process is one running plugin subprocess: the connection every plugin.v1
// call rides, the id of the process behind it, whether it is still there, and
// the kill. A supervisor holds one and replaces it when the process goes away.
type Process struct {
	// Conn is the plugin.v1 connection. Build a client over it with
	// pluginv1.NewPluginClient.
	Conn   grpc.ClientConnInterface
	client *plugin.Client
}

// ID names the running process — its pid — so a log line can say which one
// died.
func (p *Process) ID() string { return p.client.ID() }

// Exited reports whether the subprocess is gone. It is the whole exit signal
// go-plugin offers: there is no channel to select on, so a supervisor looks.
func (p *Process) Exited() bool { return p.client.Exited() }

// Kill terminates the subprocess and waits for it. Safe to call twice.
func (p *Process) Kill() { p.client.Kill() }

// LoadPlugin spawns a plugin binary and hands back the running process: the
// config map rides the spawn environment, the host pid rides with it for the
// guest's host-death watchdog.
func LoadPlugin(binaryPath string, cfg map[string]string) (*Process, error) {
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
		Stderr:           os.Stderr,
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
