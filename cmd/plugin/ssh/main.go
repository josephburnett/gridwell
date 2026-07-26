// gridwell-ssh is the remote-node mount. It opens an SSH tunnel to a remote
// host, dials the remote gridwell node's HTTP/h2c port through it, and mounts
// THE WHOLE NODE via its export (internal/server/nodeexport.go): the mount's
// root is the remote's node grid (its plugin-list landing page), and every
// remote plugin — wells, tiles, live shell PTYs (OpenShell), session blobs —
// is reachable through it by qualified id, hop by hop. This binary is a
// transparent proxy (internal/plugin/proxy); the local server's transit
// qualification (Registry.Transit) prepends this plugin's uuid to every id on
// the way back, so chains compose. The whole dial path lives in
// internal/plugin/sshdial (tested against a real in-process ssh server).
//
// Config keys (validated by sshdial.FromPluginConfig):
//
//	host:        SSH endpoint "host:port" (e.g. "example.com:22")
//	user:        SSH user
//	key:         path to the private key file
//	known_hosts: path to a known_hosts file verifying the host key (required)
//	addr:        the remote node's HTTP address AS SEEN ON THE REMOTE HOST —
//	             its server.yaml `bind:`, e.g. "127.0.0.1:8080"
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
	// NodeMount = the transparent proxy with one override: Info's network
	// context becomes THIS hop's tunnel-SOCKS endpoint, so live url tiles in
	// remote plugins browse with the remote's network (issue #24).
	guest.Serve(proxy.New(client))
}
