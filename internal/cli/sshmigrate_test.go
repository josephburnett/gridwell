package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin/sshhost"
)

// The #251 config→data migration: an old server.yaml ssh entry carrying
// connection keys becomes a named connection row in the plugin's DB, and
// the keys leave the file. Idempotent both ways — rerunning is a no-op, and
// a crash between import and rewrite (keys still present, row already
// there) must not mint a twin.
func TestMigrateSSHConfigConnections(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "server.yaml")
	const sshID = "s1a2b3c"
	yamlBody := `node_id: n0d3aaa
bind: "127.0.0.1:9999"
plugins:
    - id: ldb0001
      name: home
      kind: localdb
    - id: ` + sshID + `
      name: rtb
      kind: ssh
      config:
        addr: 127.0.0.1:10010
        host: rtb.example:2222
        key: /home/joe/.ssh/rtb
        known_hosts: /home/joe/.ssh/known_hosts
        user: joe
`
	if err := os.WriteFile(cfgPath, []byte(yamlBody), 0o600); err != nil {
		t.Fatal(err)
	}
	// The plugin DB exists (init creates it in production).
	if err := os.MkdirAll(config.DBDir(home, sshID), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sshhost.OpenDB(config.DBFile(home, sshID))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	if err := migrateSSHConfigConnections(home, cfgPath); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The yaml lost the connection keys and kept everything else.
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"host:", "user:", "key:", "known_hosts:", "addr:"} {
		if strings.Contains(string(after), gone) {
			t.Errorf("server.yaml still carries %q after migration:\n%s", gone, after)
		}
	}
	for _, kept := range []string{"rtb", "kind: ssh", sshID, "127.0.0.1:9999", "node_id: n0d3aaa", "name: home"} {
		if !strings.Contains(string(after), kept) {
			t.Errorf("server.yaml lost %q in the rewrite:\n%s", kept, after)
		}
	}

	// The DB holds the connection: the config name as the USER-OWNED name,
	// host and port split, every other key carried.
	db, err = sshhost.OpenDB(config.DBFile(home, sshID))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conns, err := db.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 1 {
		t.Fatalf("connections = %d, want 1", len(conns))
	}
	c := conns[0]
	if c.AltText != "rtb" || !c.AltUser {
		t.Errorf("name = (%q, latched=%v), want the config name rtb, user-owned", c.AltText, c.AltUser)
	}
	p, err := sshhost.ParseParams([]byte(c.Params))
	if err != nil {
		t.Fatalf("imported params unparseable: %v", err)
	}
	if p.Host != "rtb.example" || p.Port != 2222 || p.User != "joe" ||
		p.Addr != "127.0.0.1:10010" || p.Key != "/home/joe/.ssh/rtb" {
		t.Errorf("params = %+v, want the config values with host:port split", p)
	}

	// Rerun: a no-op (keys already gone).
	if err := migrateSSHConfigConnections(home, cfgPath); err != nil {
		t.Fatal(err)
	}

	// Crash simulation: the keys come back (rewrite failed last boot) but
	// the row exists — the canonical-params dedup must skip, not twin.
	if err := os.WriteFile(cfgPath, []byte(yamlBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateSSHConfigConnections(home, cfgPath); err != nil {
		t.Fatal(err)
	}
	conns, err = db.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 1 {
		t.Fatalf("after crash-replay: connections = %d, want still 1 (no twin)", len(conns))
	}
}
