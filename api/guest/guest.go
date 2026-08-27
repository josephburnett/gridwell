// Package guest is called by a provider binary's main() to serve the
// contentprovider.v1 service over go-plugin's managed subprocess
// transport.
//
// Usage in a provider binary:
//
//	func main() {
//	    guest.ServeProvider(myImpl)
//	}
package guest

import (
	"encoding/json"
	"os"
	"strconv"
	"syscall"
	"time"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"

	gplug "github.com/josephburnett/gridwell/api/compose"
	contentproviderv1 "github.com/josephburnett/gridwell/api/gen/contentprovider/v1"
)

// Config returns the config map the host handed this plugin at spawn (db_file,
// root/pid, uuid, …), decoded from the GRIDWELL_PLUGIN_CONFIG environment
// variable. An unset/empty value yields an empty map. This replaces the old
// Attach config map: a plugin is configured once, at launch.
func Config() map[string]string {
	raw := os.Getenv(gplug.ConfigEnvVar)
	if raw == "" {
		return map[string]string{}
	}
	out := map[string]string{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// watchHost exits the guest when the spawning host dies (issue #197).
// go-plugin v1.8 gives a guest NO host-death detection in our configuration:
// the guest inherits the host's stdin (never closes), and a crashed or
// SIGKILLed host just looks like a disconnected gRPC client while the guest
// keeps listening forever — nine generations of orphaned plugins were found
// reparented to init. The host hands its pid in the environment (spawn-time
// fact, so a pre-watchdog race cannot capture a post-death parent); the
// guest probes it with signal 0 — robust against subreaper reparenting,
// where a Getppid comparison can lie. A vanished env var (a hand-launched
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

func ServeProvider(impl contentproviderv1.ContentProviderServer) {
	watchHost()
	logger := hclog.New(&hclog.LoggerOptions{
		Level:      hclog.Error,
		Output:     os.Stderr,
		JSONFormat: true,
	})
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: gplug.HandshakeConfig,
		Plugins:         gplug.ProviderPluginMap(impl),
		GRPCServer:      plugin.DefaultGRPCServer,
		Logger:          logger,
	})
}
