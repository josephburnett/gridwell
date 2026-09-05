package connection

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/internal/config"
)

// newTestServer builds a transport with no connections and no dialer.
func newTestServer(t *testing.T, db *DB) *Server {
	t.Helper()
	s, err := New(db, nil, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func openConnDB(t *testing.T) *DB {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "remote.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// server.yaml is authoritative about what is DECLARED; retired_names is the
// one owner of what is RETIRED. A declared name gets a row; a name the config
// merely drops keeps its row and its learned landing and comes back with the
// stanza; a retired name is reserved even on a fresh store and never returns.
func TestRetirementIsExplicitAndAbsenceIsNot(t *testing.T) {
	ctx := context.Background()
	db := openConnDB(t)
	s, err := New(db, nil, "", []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}, {Name: "rtb", Addr: "/t"}}, []string{"olddead"})
	if err != nil {
		t.Fatal(err)
	}
	rows := s.Rows(ctx)
	if len(rows) != 2 || rows[0].Name != "geneva" || rows[0].Label != "geneva" || rows[1].Name != "rtb" {
		t.Fatalf("rows = %+v", rows)
	}
	if r, _ := db.Get(ctx, "olddead"); !r.Deleted {
		t.Fatal("retired_names must be reserved in the store")
	}
	// A landing the transport learned, so the surviving row has something to
	// lose.
	if err := db.SetRemoteRoot(ctx, "rtb", "rnode1/3"); err != nil {
		t.Fatal(err)
	}
	// Drop rtb from the config: the row survives untouched. Boot never
	// retires on absence.
	if _, err := New(db, nil, "", []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, []string{"olddead"}); err != nil {
		t.Fatal(err)
	}
	r, err := db.Get(ctx, "rtb")
	if err != nil {
		t.Fatalf("an undeclared connection's row must survive: %v", err)
	}
	if r.Deleted {
		t.Fatal("boot must never tombstone on absence — retirement lives in retired_names")
	}
	if r.RemoteRoot != "rnode1/3" {
		t.Fatalf("remote_root = %q, want the learned landing intact", r.RemoteRoot)
	}
	// Re-declare it: the connection comes back, landing and all.
	s2, err := New(db, nil, "", []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}, {Name: "rtb", Addr: "/t"}}, []string{"olddead"})
	if err != nil {
		t.Fatalf("a name the config merely dropped must be declarable again: %v", err)
	}
	rows = s2.Rows(ctx)
	if len(rows) != 2 || rows[1].Name != "rtb" || rows[1].RootGridID != "rtb/rnode1/3" {
		t.Fatalf("rows = %+v, want rtb back on its remembered landing", rows)
	}
	if _, err := New(db, nil, "", []config.ConnectionConfig{{Name: "olddead", Addr: "/t"}}, []string{"olddead"}); err == nil || !strings.Contains(err.Error(), "RETIRED") {
		t.Fatalf("a retired name must never return, got %v", err)
	}
	if _, err := New(db, nil, "", []config.ConnectionConfig{{Name: "42", Addr: "/t"}}, nil); err == nil {
		t.Fatal("a numeric name is not a namespace segment")
	}
}

// The `deleted` column is the mirror of retired_names and nothing else. A
// tombstone the OLD boot reconcile wrote — one boot with the stanza absent —
// is a stale mirror, not a retirement, so the next boot clears it and the
// mounts through that name come back. No SQL by hand.
func TestStaleTombstonesHealAndRetiredNamesMirror(t *testing.T) {
	ctx := context.Background()
	db := openConnDB(t)
	if err := db.Tombstone(ctx, "laptop"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRemoteRoot(ctx, "laptop", "rnode1/3"); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db, nil, "", []config.ConnectionConfig{{Name: "laptop", Addr: "/s"}}, nil); err != nil {
		t.Fatalf("a declared name the old reconcile tombstoned must heal, not refuse: %v", err)
	}
	r, _ := db.Get(ctx, "laptop")
	if r.Deleted {
		t.Fatal("the stale tombstone must be cleared")
	}
	if r.RemoteRoot != "rnode1/3" {
		t.Fatalf("remote_root = %q, want the landing kept through the heal", r.RemoteRoot)
	}
	// An UNdeclared row heals too: the mirror is retired_names, whether the
	// name is declared this boot or not.
	if err := db.Tombstone(ctx, "ghost"); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db, nil, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if r, _ := db.Get(ctx, "ghost"); r.Deleted {
		t.Fatal("only retired_names retires a name")
	}
	// And retired_names is mirrored onto the row, declared or not, so route
	// and Probe can read retirement off it.
	if _, err := New(db, nil, "", nil, []string{"ghost"}); err != nil {
		t.Fatal(err)
	}
	if r, _ := db.Get(ctx, "ghost"); !r.Deleted {
		t.Fatal("retired_names must be mirrored onto the row")
	}
	if _, err := New(db, nil, "", []config.ConnectionConfig{{Name: "ghost", Addr: "/s"}}, []string{"ghost"}); err == nil || !strings.Contains(err.Error(), "RETIRED") {
		t.Fatalf("declaring a retired name must be refused loudly, got %v", err)
	}
}

// dialConfig applies the host defaults: direct dial needs only addr; ssh
// needs user, defaults port 22 and the ~/.ssh files.
func TestDialConfigDefaults(t *testing.T) {
	home := t.TempDir()
	touch(t, filepath.Join(home, "k"))
	touch(t, filepath.Join(home, ".ssh", "known_hosts"))
	s := &Server{home: home}
	if _, err := s.dialConfig(config.ConnectionConfig{Name: "a", Host: "h", User: "u"}); err == nil || !strings.Contains(err.Error(), "addr required") {
		t.Fatalf("addr is required: %v", err)
	}
	d, err := s.dialConfig(config.ConnectionConfig{Name: "a", Addr: "/sock"})
	if err != nil || d.Host != "" || d.Addr != "/sock" {
		t.Fatalf("direct dial = %+v (%v)", d, err)
	}
	if _, err := s.dialConfig(config.ConnectionConfig{Name: "a", Host: "h", Addr: "/sock"}); err == nil {
		t.Fatal("ssh needs a user")
	}
	d, err = s.dialConfig(config.ConnectionConfig{Name: "a", Host: "h", User: "u", Addr: "/sock", Key: "~/k"})
	if err != nil || d.Host != "h:22" || d.KeyPath != filepath.Join(home, "k") || d.KnownHosts != filepath.Join(home, ".ssh", "known_hosts") {
		t.Fatalf("ssh defaults = %+v (%v)", d, err)
	}
	if _, err := s.dialConfig(config.ConnectionConfig{Name: "a", Host: "h", User: "u", Addr: "/sock", Port: 70000}); err == nil {
		t.Fatal("port range")
	}
}

// The dial plan is checked, not merely resolved: every host-local file it
// names must be there. A typo in key: or known_hosts: is a fact this machine
// can settle, and it is settled here, at the one place config becomes a dial
// plan, so the boot fails instead of the connection going quietly dark. The
// error names the field and the exact path.
func TestDialConfigRefusesAPathThatIsNotThere(t *testing.T) {
	home := t.TempDir()
	key := filepath.Join(home, "k")
	kh := filepath.Join(home, "kh")
	s := &Server{home: home}
	ssh := config.ConnectionConfig{Name: "a", Host: "h", User: "u", Addr: "/sock", Key: key, KnownHosts: kh}

	_, err := s.dialConfig(ssh)
	if err == nil || !strings.Contains(err.Error(), "key") || !strings.Contains(err.Error(), key) {
		t.Fatalf("a key that is not there must be refused by path: %v", err)
	}
	touch(t, key)
	_, err = s.dialConfig(ssh)
	if err == nil || !strings.Contains(err.Error(), "known_hosts") || !strings.Contains(err.Error(), kh) {
		t.Fatalf("a known_hosts that is not there must be refused by path: %v", err)
	}
	touch(t, kh)
	if _, err := s.dialConfig(ssh); err != nil {
		t.Fatalf("both files present: %v", err)
	}

	// A direct dial opens no host-local file. Whether the far socket is
	// there is a network fact, not a config one, so it is not checked.
	if _, err := s.dialConfig(config.ConnectionConfig{Name: "a", Addr: filepath.Join(home, "no-such.sock")}); err != nil {
		t.Fatalf("a direct dial of an absent socket is a dark remote, not a misconfiguration: %v", err)
	}
}

// New is the boot gate: a declared connection whose host-local config cannot
// resolve fails transport construction, which is what fails node.Start and so
// `serve`. The error names the connection.
func TestNewRefusesAMisconfiguredConnection(t *testing.T) {
	home := t.TempDir()
	key := filepath.Join(home, "nope", "id_ed25519")
	_, err := New(openConnDB(t), nil, home, []config.ConnectionConfig{
		{Name: "geneva", Host: "h", User: "u", Addr: "/sock", Key: key, KnownHosts: key},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "geneva") || !strings.Contains(err.Error(), key) {
		t.Fatalf("the transport must refuse to build, naming the connection and the path: %v", err)
	}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}
