// gridwell-ssh is the remote-node plugin: connections are DATA (issues
// #199, #251), never config. The plugin serves its connection list as its
// INSTANCE grid; the client's instance picker creates connections there
// (params commit as each well's content), the plugin dials lazily, and a
// well's child is the remote's node grid. Each connection gets a minted
// letter-leading short id as a SUB-NAMESPACE segment
// (`<ssh-plugin>/<conn>/<remote-plugin>/<id>`): the plugin peels its
// connection segment exactly as a node peels a plugin segment, so
// namespaces recurse and server.yaml never names hosts. Implementation:
// internal/plugin/sshhost.
//
// The old config-pinned single-mount mode (`host:` in server.yaml) is gone;
// `gridwell serve` migrates such an entry into a connection row at boot, so
// this binary refuses the keys outright — seeing them means the migration
// was bypassed.
package main

import (
	"fmt"
	"os"

	"github.com/josephburnett/gridwell/internal/plugin/guest"
	"github.com/josephburnett/gridwell/internal/plugin/pluginmeta"
	"github.com/josephburnett/gridwell/internal/plugin/sshdial"
	"github.com/josephburnett/gridwell/internal/plugin/sshhost"
)

func main() {
	cfg := guest.Config()
	if cfg["host"] != "" {
		fmt.Fprintln(os.Stderr, "gridwell-ssh: connection config keys are retired (#251) — connections are data; `gridwell serve` migrates old entries at boot")
		os.Exit(1)
	}
	// The ssh plugin owns a local DB like every plugin — its durable identity
	// (pluginmeta) and the connection rows themselves. Verify id+kind against
	// that DB before serving anything.
	dbPath := cfg["db_file"]
	if dbPath == "" {
		fmt.Fprintln(os.Stderr, "gridwell-ssh: db_file config key required (connections persist there)")
		os.Exit(1)
	}
	if _, err := pluginmeta.Verify(dbPath, cfg["uuid"], cfg["kind"]); err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-ssh: %v\n", err)
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
}
