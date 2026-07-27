// gridwell-ssh is the remote-node plugin. It has two modes, selected by
// config:
//
// CONNECTIONS MODE (no `host:` key — the default; issue #199): connections
// are DATA, not config. The plugin serves its own root grid; dropping a
// connection WELL there prompts (via the #198 creation schema) for host,
// user, key path, … — the params commit as the well's content, the plugin
// dials, and the well's child is the remote's node grid. Each connection
// gets a minted letter-leading short id as a SUB-NAMESPACE segment
// (`<ssh-plugin>/<conn>/<remote-plugin>/<id>`): the plugin peels its
// connection segment exactly as a node peels a plugin segment, so
// namespaces recurse and server.yaml stops naming hosts. Implementation:
// internal/plugin/sshhost.
//
// CONFIG-PINNED MODE (`host:` present — the pre-#199 shape, kept working):
// one plugin entry mounts ONE remote node. The plugin opens an SSH tunnel,
// dials the remote gridwell node's HTTP/h2c port through it, and mounts THE
// WHOLE NODE via its export (internal/server/nodeexport.go): the mount's
// root is the remote's node grid, and every remote plugin is reachable
// through it by qualified id, hop by hop, via the transparent proxy
// (internal/plugin/proxy).
//
// Config keys (config-pinned mode; validated by sshdial.FromPluginConfig):
//
//	host:        SSH endpoint "host:port" (e.g. "example.com:22")
//	user:        SSH user
//	key:         path to the private key file
//	known_hosts: path to a known_hosts file verifying the host key (required)
//	addr:        the remote node's HTTP address AS SEEN ON THE REMOTE HOST —
//	             its server.yaml `bind:`, e.g. "127.0.0.1:8080"
//
// In connections mode those keys move into each connection's params
// document; the plugin needs only its db_file (where connections persist).
package main

import (
	"fmt"
	"os"

	"github.com/josephburnett/gridwell/internal/plugin/guest"
	"github.com/josephburnett/gridwell/internal/plugin/pluginmeta"
	"github.com/josephburnett/gridwell/internal/plugin/proxy"
	"github.com/josephburnett/gridwell/internal/plugin/sshdial"
	"github.com/josephburnett/gridwell/internal/plugin/sshhost"
)

func main() {
	cfg := guest.Config()
	// The ssh plugin owns a local DB like every plugin — its durable identity
	// (pluginmeta), and in connections mode the connection rows themselves.
	// Verify id+kind against that DB before serving anything.
	if dbPath := cfg["db_file"]; dbPath != "" {
		if _, err := pluginmeta.Verify(dbPath, cfg["uuid"], cfg["kind"]); err != nil {
			fmt.Fprintf(os.Stderr, "gridwell-ssh: %v\n", err)
			os.Exit(1)
		}
	}

	if cfg["host"] == "" {
		// Connections mode: remote nodes are wells the user drops, persisted
		// in this plugin's own DB.
		dbPath := cfg["db_file"]
		if dbPath == "" {
			fmt.Fprintln(os.Stderr, "gridwell-ssh: db_file config key required (connections mode persists connection wells there)")
			os.Exit(1)
		}
		db, err := sshhost.OpenDB(dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gridwell-ssh: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()
		home, _ := os.UserHomeDir()
		srv := sshhost.New(db, sshdial.Dial, home)
		defer srv.Close()
		guest.Serve(srv)
		return
	}

	// Config-pinned mode: one entry, one remote node.
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
