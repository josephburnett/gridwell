// Package guest is called by a plugin binary's main() to serve the
// plugin.v1 service over go-plugin's managed subprocess
// transport.
//
// Usage in a plugin binary:
//
//	func main() {
//	    guest.Main(myplugin.FromConfig)
//	}
//
// where FromConfig is the plugin's one config→plugin derivation.
package guest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gplug "github.com/josephburnett/gridwell/api/compose"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
)

// Config returns the config map the host handed this plugin at spawn,
// decoded from the GRIDWELL_PLUGIN_CONFIG environment variable. An
// unset or empty value yields an empty map. A value that is not a JSON
// object is an error, never an empty map: a plugin that silently ran
// unconfigured (fs rootless, proc at pid 1) would look like a plugin that
// lost its config. A plugin is configured once, at launch.
func Config() (map[string]string, error) {
	raw := os.Getenv(gplug.ConfigEnvVar)
	if raw == "" {
		return map[string]string{}, nil
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("%s is not a JSON object of strings: %v", gplug.ConfigEnvVar, err)
	}
	return out, nil
}

// Factory is the plugin's config→plugin derivation: the one owner of how
// server.yaml config becomes a running plugin, and the argument Main takes.
// An error is the verdict "I do not have the config I need".
type Factory func(cfg map[string]string) (pluginv1.PluginServer, error)

// Main decodes the spawn config, builds the plugin, and serves it. A
// config that will not decode, or a factory that refuses it, is served as
// a refusal: a plugin whose Info answers FailedPrecondition with the
// reason, so the host stops the launch naming it. Exiting instead would
// present as a handshake failure with the reason lost in the guest's
// stderr.
func Main(factory Factory) {
	Serve(build(factory))
}

// build is Main without the serving: the decode + factory + refusal
// derivation, testable in-process.
func build(factory Factory) pluginv1.PluginServer {
	cfg, err := Config()
	if err != nil {
		return refusal{err: err}
	}
	impl, err := factory(cfg)
	if err != nil {
		return refusal{err: err}
	}
	return impl
}

// refusal is the plugin that could not be built: Info carries the reason
// as FailedPrecondition; every other verb is unimplemented.
type refusal struct {
	pluginv1.UnimplementedPluginServer
	err error
}

func (r refusal) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return nil, status.Errorf(codes.FailedPrecondition, "%v", r.err)
}

// watchHost exits the guest when the spawning host dies. go-plugin gives a
// guest no host-death detection in our configuration: the guest inherits
// the host's stdin (which never closes), and a killed host looks like a
// disconnected gRPC client while the guest keeps listening forever. The
// host hands its pid in the environment — a spawn-time fact, so a
// pre-watchdog race cannot capture a post-death parent — and the guest
// probes it with signal 0, which is robust against subreaper reparenting
// where a Getppid comparison can lie. A missing env var (a hand-launched
// guest, a test harness) disables the watchdog rather than guessing.
func watchHost() {
	pid, err := strconv.Atoi(os.Getenv(gplug.HostPIDEnvVar))
	if err != nil || pid <= 0 {
		return
	}
	go func() {
		for {
			time.Sleep(2 * time.Second)
			// Signal 0 probes existence; EPERM still means alive. Only
			// ESRCH — no such process — is the host-death verdict.
			if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
				os.Exit(0)
			}
		}
	}()
}

// Serve serves impl over the managed subprocess transport. Main is the
// usual door; Serve is for a main that builds its plugin some other way.
func Serve(impl pluginv1.PluginServer) {
	watchHost()
	logger := hclog.New(&hclog.LoggerOptions{
		Level:      hclog.Error,
		Output:     os.Stderr,
		JSONFormat: true,
	})
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: gplug.HandshakeConfig,
		Plugins:         gplug.PluginMap(impl),
		GRPCServer:      plugin.DefaultGRPCServer,
		Logger:          logger,
	})
}
