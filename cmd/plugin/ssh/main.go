// gridwell-ssh is the remote-gateway plugin binary. It opens an SSH tunnel to
// a remote host, dials the remote gridwell node's HTTP/h2c port through it,
// and mounts ONE of the remote's plugins via the node export
// (internal/server/nodeexport.go): every call carries the gridwell-plugin
// selector, so the far end answers with that plugin's own service verbatim —
// local ids, its wells, tiles, live shell PTYs (OpenShell), and session blob
// all reach the local server through a transparent proxy
// (internal/plugin/proxy), same as a local plugin. "Remote" is just a
// transport; the whole dial/resolve path lives in internal/plugin/sshdial
// (where it is tested against a real in-process ssh server).
//
// Config keys (validated by sshdial.FromPluginConfig):
//
//	host:          SSH endpoint "host:port" (e.g. "example.com:22")
//	user:          SSH user
//	key:           path to the private key file
//	known_hosts:   path to a known_hosts file verifying the host key (required)
//	addr:          the remote node's HTTP address AS SEEN ON THE REMOTE HOST —
//	               its server.yaml `bind:`, e.g. "127.0.0.1:8080"
//	remote_plugin: which remote plugin to mount (config name or uuid);
//	               optional when the remote has exactly one plugin
package main

import (
	"fmt"
	"os"

	"github.com/josephburnett/gridwell/internal/plugin/guest"
	"github.com/josephburnett/gridwell/internal/plugin/pluginmeta"
	"github.com/josephburnett/gridwell/internal/plugin/proxy"
	"github.com/josephburnett/gridwell/internal/plugin/sshdial"
)

func main() {
	cfg := guest.Config()
	// The ssh plugin is a transparent proxy, but it still owns a local DB like
	// every plugin — it records its durable identity (and may later stash keys
	// etc.) there. Verify id+kind against that DB before dialing out.
	if dbPath := cfg["db_file"]; dbPath != "" {
		if _, err := pluginmeta.Verify(dbPath, cfg["uuid"], cfg["kind"]); err != nil {
			fmt.Fprintf(os.Stderr, "gridwell-ssh: %v\n", err)
			os.Exit(1)
		}
	}
	dial, err := sshdial.FromPluginConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-ssh: %v\n", err)
		os.Exit(1)
	}
	client, closer, err := sshdial.Dial(dial)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-ssh: %v\n", err)
		os.Exit(1)
	}
	defer closer()
	guest.Serve(proxy.New(client))
}
