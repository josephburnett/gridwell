package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/internal/config"
)

// startCfg is a fresh home's config, bound to an ephemeral port.
func startCfg(t *testing.T) (string, *config.ServerConfig) {
	t.Helper()
	home := t.TempDir()
	cfg, err := BuildConfig(home, filepath.Join(home, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Web.Bind = "127.0.0.1:0"
	return home, cfg
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A host-local config fact this machine can settle must fail the boot. An ssh
// connection whose key: is a typo can never dial, so serve refuses to start
// and says which connection and which path — the alternative, and what the
// node used to do, is a server that comes up happily with the connection
// quietly dark.
func TestStartRefusesAConnectionKeyThatIsNotThere(t *testing.T) {
	home, cfg := startCfg(t)
	key := filepath.Join(home, "keys", "id_ed25519") // never written
	cfg.Connections = []config.ConnectionConfig{{
		Name: "geneva", Host: "far.example", User: "joe",
		Addr: "/far/federation.sock",
		Key:  key, KnownHosts: filepath.Join(home, "known_hosts"),
	}}
	n, err := Start(Options{Home: home, Cfg: cfg})
	if err == nil {
		n.Close()
		t.Fatal("serve started with a key path that is not there")
	}
	if !strings.Contains(err.Error(), "geneva") || !strings.Contains(err.Error(), key) {
		t.Fatalf("the error must name the connection and the exact path, got: %v", err)
	}
}

// The same for known_hosts: the check is over every host-local file the dial
// plan names, not over the one field somebody remembered.
func TestStartRefusesAKnownHostsThatIsNotThere(t *testing.T) {
	home, cfg := startCfg(t)
	key := filepath.Join(home, "id_ed25519")
	writeFile(t, key, "not a real key, but it is readable")
	kh := filepath.Join(home, "known_hosts") // never written
	cfg.Connections = []config.ConnectionConfig{{
		Name: "geneva", Host: "far.example", User: "joe",
		Addr: "/far/federation.sock",
		Key:  key, KnownHosts: kh,
	}}
	n, err := Start(Options{Home: home, Cfg: cfg})
	if err == nil {
		n.Close()
		t.Fatal("serve started with a known_hosts path that is not there")
	}
	if !strings.Contains(err.Error(), "geneva") || !strings.Contains(err.Error(), kh) {
		t.Fatalf("the error must name the connection and the exact path, got: %v", err)
	}
}

// An unreadable key is as dead as an absent one, and just as checkable here.
func TestStartRefusesAConnectionKeyItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file; the permission half of the check needs an ordinary uid")
	}
	home, cfg := startCfg(t)
	key := filepath.Join(home, "id_ed25519")
	writeFile(t, key, "secret")
	if err := os.Chmod(key, 0); err != nil {
		t.Fatal(err)
	}
	kh := filepath.Join(home, "known_hosts")
	writeFile(t, kh, "far.example ssh-ed25519 AAAA\n")
	cfg.Connections = []config.ConnectionConfig{{
		Name: "geneva", Host: "far.example", User: "joe",
		Addr: "/far/federation.sock",
		Key:  key, KnownHosts: kh,
	}}
	n, err := Start(Options{Home: home, Cfg: cfg})
	if err == nil {
		n.Close()
		t.Fatal("serve started with a key it cannot read")
	}
	if !strings.Contains(err.Error(), "geneva") || !strings.Contains(err.Error(), key) {
		t.Fatalf("the error must name the connection and the exact path, got: %v", err)
	}
}

// addr is required either way — the far node's connection-door socket path is
// something only the operator knows. A row without one could never dial, so it
// fails the boot rather than the first read.
func TestStartRefusesAConnectionWithNoAddr(t *testing.T) {
	home, cfg := startCfg(t)
	cfg.Connections = []config.ConnectionConfig{{Name: "geneva", Label: "rtb"}}
	n, err := Start(Options{Home: home, Cfg: cfg})
	if err == nil {
		n.Close()
		t.Fatal("serve started with a connection that declares no addr")
	}
	if !strings.Contains(err.Error(), "geneva") || !strings.Contains(err.Error(), "addr") {
		t.Fatalf("the error must name the connection and the missing field, got: %v", err)
	}
}

// The boundary, from the other side: a remote that does not answer is a
// NETWORK fact, and offline boot is decided behavior — a laptop on a plane
// serves its home and its cache. The connection stays dark at runtime, with
// its reason on its menu row, and the node serves.
func TestStartServesWhenTheRemoteIsMerelyUnreachable(t *testing.T) {
	home, cfg := startCfg(t)
	cfg.Connections = []config.ConnectionConfig{{
		Name: "geneva", Label: "rtb",
		Addr: filepath.Join(home, "not-running", "federation.sock"),
	}}
	n, err := Start(Options{Home: home, Cfg: cfg})
	if err != nil {
		t.Fatalf("an unreachable remote must not fail the boot: %v", err)
	}
	defer n.Close()
	rows := n.Reg.Connections(t.Context())
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].RootGridID != "" || rows[0].StatusDetail == "" {
		t.Fatalf("a dark connection must be pending with its reason on the row, got %+v", rows[0])
	}
}
