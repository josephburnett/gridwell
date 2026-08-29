package remote

import (
	"context"
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

// server.yaml is authoritative: a declared name gets a row; a name the
// config dropped tombstones; a retired name is reserved even on a fresh
// store; and none of the three ever comes back.
func TestReconcileDeclaredRetiredAndDropped(t *testing.T) {
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
	// Drop rtb from the config: it tombstones; re-declaring it is refused.
	if _, err := New(db, nil, "", []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil); err != nil {
		t.Fatal(err)
	}
	if r, _ := db.Get(ctx, "rtb"); !r.Deleted {
		t.Fatal("a dropped connection must tombstone")
	}
	if _, err := New(db, nil, "", []config.ConnectionConfig{{Name: "rtb", Addr: "/t"}}, nil); err == nil || !strings.Contains(err.Error(), "RETIRED") {
		t.Fatalf("a tombstoned name must never return, got %v", err)
	}
	if _, err := New(db, nil, "", []config.ConnectionConfig{{Name: "olddead", Addr: "/t"}}, []string{"olddead"}); err == nil || !strings.Contains(err.Error(), "RETIRED") {
		t.Fatalf("a retired name must never return, got %v", err)
	}
	if _, err := New(db, nil, "", []config.ConnectionConfig{{Name: "42", Addr: "/t"}}, nil); err == nil {
		t.Fatal("a numeric name is not a namespace segment")
	}
}

// dialConfig applies the host defaults: direct dial needs only addr; ssh
// needs user, defaults port 22 and the ~/.ssh files.
func TestDialConfigDefaults(t *testing.T) {
	home := t.TempDir()
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
