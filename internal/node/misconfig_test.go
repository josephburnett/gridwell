package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
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

// A host-local config fact this machine can settle must fail the boot: an ssh
// connection whose key: is a typo can never dial, so serve refuses to start
// and names the connection and the path.
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

// The check covers every host-local file the dial plan names.
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

// addr is required either way: only the operator knows the far node's socket
// path, so a row without one fails the boot rather than the first read.
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

// A plugin whose binary: path is not there fails the boot too, naming the
// path: a plugin without the binary it needs must never come up as an empty
// grid.
func TestStartRefusesAPluginBinaryThatIsNotThere(t *testing.T) {
	home, cfg := startCfg(t)
	bin := filepath.Join(home, "nowhere", "gridwell-plugin-fs")
	cfg.Plugins = []config.PluginConfig{{ID: "aaaaaaa", Kind: "fs", Binary: bin}}
	n, err := Start(Options{Home: home, Cfg: cfg})
	if err == nil {
		n.Close()
		t.Fatal("serve started with a plugin binary that is not there")
	}
	if !strings.Contains(err.Error(), bin) {
		t.Fatalf("the error must name the exact path, got: %v", err)
	}
}

// A remote that does not answer is a network fact, not a config one: the node
// serves and the connection stays dark at runtime with its reason on its row.
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
	transport, ok := n.Reg.Transport()
	if !ok {
		t.Fatal("no transport installed")
	}
	hs, err := transport.Handshake(t.Context(), &pb.HandshakeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	rows := hs.Connections
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].RootGridId != "" || rows[0].StatusDetail == "" {
		t.Fatalf("a dark connection must be pending with its reason on the row, got %+v", rows[0])
	}
}

// A home still in the db/<id>/ layout keeps all the user's content there, so
// serve refuses it and names the release that folds it in rather than minting
// a fresh gridwell.db beside those files.
func TestStartRefusesTheOldPerNamespaceLayout(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "server.yaml")
	writeFile(t, cfgPath, "id: nkw3zq7\n")
	old := filepath.Join(home, "db", "nkw3zq7")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(old, "store.db"), "the user's content")

	_, err := BuildConfig(home, cfgPath)
	if err == nil {
		t.Fatal("serve accepted a home in the old layout")
	}
	if !strings.Contains(err.Error(), "v0.1.0") || !strings.Contains(err.Error(), "db/") {
		t.Fatalf("the refusal must name the old layout and the release that converts it, got: %v", err)
	}
	if _, err := os.Stat(config.DBFile(home)); !os.IsNotExist(err) {
		t.Fatalf("%s was minted beside the old layout", config.DBFile(home))
	}
}
