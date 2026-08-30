package compose

// The content-plugin half of compose: a plugin binary serves plugin.v1.
// PluginInProcess is the compiled-in shape — a real gRPC loopback on an
// in-memory listener, so the caller holds the same client interface a
// subprocess dial would give.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
)

// ConfigEnvVar is the environment variable the host uses to hand a
// plugin its config map (JSON) at spawn — guest.Config reads it.
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

func (p *pluginGRPCPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return pluginv1.NewPluginClient(c), nil
}

// PluginMap is the plugin map for plugin binaries — impl set on
// the guest side, nil on the host side.
func PluginMap(impl pluginv1.PluginServer) map[string]plugin.Plugin {
	return map[string]plugin.Plugin{
		PluginName: &pluginGRPCPlugin{Impl: impl},
	}
}

// LoadPlugin spawns a plugin binary and hands back the connected
// client: the config map rides the spawn environment (guest.Config), the
// host pid rides with it for the guest's host-death watchdog.
func LoadPlugin(binaryPath string, cfg map[string]string) (pluginv1.PluginClient, func(), error) {
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
		return nil, nil, fmt.Errorf("plugin dial %q: %w", binaryPath, err)
	}
	raw, err := rpcClient.Dispense(PluginName)
	if err != nil {
		client.Kill()
		return nil, nil, fmt.Errorf("plugin dispense %q: %w", binaryPath, err)
	}
	cp, ok := raw.(pluginv1.PluginClient)
	if !ok {
		client.Kill()
		return nil, nil, fmt.Errorf("plugin %q: unexpected type %T", binaryPath, raw)
	}
	return cp, client.Kill, nil
}

// PluginInProcess serves a Plugin implementation over a
// loopback gRPC server and returns the connected client — the plugin
// twin of the subprocess dial.
func PluginInProcess(impl pluginv1.PluginServer) (pluginv1.PluginClient, func(), error) {
	// In-memory listener: no socket, no reach from
	// outside the process.
	lis := bufconn.Listen(1 << 20)

	srv := grpc.NewServer()
	pluginv1.RegisterPluginServer(srv, impl)
	go srv.Serve(lis)

	cc, err := grpc.NewClient("passthrough:///in-process",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	if err != nil {
		srv.Stop()
		return nil, nil, fmt.Errorf("in-process plugin dial: %w", err)
	}

	closer := func() {
		cc.Close()
		srv.GracefulStop()
	}
	return pluginv1.NewPluginClient(cc), closer, nil
}
