// Package guest is called by a plugin binary's main() to serve the Gridwell
// gRPC service over go-plugin's managed subprocess transport.
//
// Usage in a plugin binary:
//
//	func main() {
//	    guest.Serve(myImpl)
//	}
package guest

import (
	"os"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	gplug "github.com/josephburnett/gridwell/internal/plugin"
)

// Serve runs the plugin event loop. It blocks until the host process closes
// the connection or the process is killed. impl must implement
// gridwellv1.GridwellServer; embed gridwellv1.UnimplementedGridwellServer and
// override the methods your plugin supports.
func Serve(impl gridwellv1.GridwellServer) {
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
