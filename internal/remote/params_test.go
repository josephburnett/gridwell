package remote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseParamsAuthoritativeValidation(t *testing.T) {
	if _, err := ParseParams([]byte(`{"user":"u"}`)); err == nil || !strings.Contains(err.Error(), "host") {
		t.Errorf("missing host must be refused: %v", err)
	}
	if _, err := ParseParams([]byte(`{"host":"h"}`)); err == nil || !strings.Contains(err.Error(), "user") {
		t.Errorf("missing user must be refused: %v", err)
	}
	if _, err := ParseParams([]byte(`{"host":"h","user":"u","port":70000}`)); err == nil {
		t.Error("out-of-range port must be refused")
	}
	if _, err := ParseParams([]byte(`{"host":"h","user":"u","port":22.5}`)); err == nil {
		t.Error("fractional port must be refused")
	}
	if _, err := ParseParams([]byte(`{"host":"h/x","user":"u"}`)); err == nil {
		t.Error("a host containing '/' must be refused (it would corrupt chained ids)")
	}
	if _, err := ParseParams([]byte(`not json`)); err == nil {
		t.Error("garbage must be refused")
	}
	// Unknown keys are tolerated: a newer client may say more.
	if _, err := ParseParams([]byte(`{"host":"h","user":"u","x-later":true}`)); err != nil {
		t.Errorf("unknown keys must be tolerated: %v", err)
	}
}

func TestDialConfigDefaults(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Only id_rsa exists: the key default must pick the first EXISTING path.
	if err := os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}

	// addr has NO default (2026-08-26): the remote's federation socket
	// lives under its home, which only the operator knows.
	p, err := ParseParams([]byte(`{"host":"rtb","user":"joe"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DialConfig(home); err == nil || !strings.Contains(err.Error(), "addr required") {
		t.Fatalf("missing addr must refuse naming the socket: %v", err)
	}
	p, err = ParseParams([]byte(`{"host":"rtb","user":"joe","addr":"/home/joe/.gridwell/federation.sock"}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := p.DialConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "rtb:22" {
		t.Errorf("default port: host = %q, want rtb:22", cfg.Host)
	}
	if cfg.Addr != "/home/joe/.gridwell/federation.sock" {
		t.Errorf("addr = %q", cfg.Addr)
	}
	if cfg.KeyPath != filepath.Join(sshDir, "id_rsa") {
		t.Errorf("key default = %q, want the existing id_rsa", cfg.KeyPath)
	}
	if cfg.KnownHosts != filepath.Join(sshDir, "known_hosts") {
		t.Errorf("known_hosts default = %q", cfg.KnownHosts)
	}

	// Explicit values win over every default.
	p, err = ParseParams([]byte(`{"host":"rtb","user":"joe","port":2222,"addr":"10.0.0.5:9","key":"/kp","known_hosts":"/kh"}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = p.DialConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "rtb:2222" || cfg.Addr != "10.0.0.5:9" || cfg.KeyPath != "/kp" || cfg.KnownHosts != "/kh" {
		t.Errorf("explicit params overridden: %+v", cfg)
	}

	// No home dir and no explicit paths: refuse loudly rather than guess.
	p, _ = ParseParams([]byte(`{"host":"rtb","user":"joe","addr":"/r/federation.sock"}`))
	if _, err := p.DialConfig(""); err == nil {
		t.Error("no home and no key path must be refused")
	}
}

// TestDialConfigExpandsUserSuppliedTilde reproduces the bug where a
// user-typed "~/.ssh/foo_key" (matching the schema's own placeholder text,
// params.go's CreateSchemaWell) was passed to os.ReadFile verbatim instead
// of being resolved against home — only the auto-defaulted path ever got
// that treatment. A fully-qualified path worked; "~/..." silently didn't.
func TestDialConfigExpandsUserSuppliedTilde(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(sshDir, "foo_key")
	if err := os.WriteFile(keyPath, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := ParseParams([]byte(`{"host":"rtb","user":"joe","addr":"/r/federation.sock","key":"~/.ssh/foo_key","known_hosts":"~/.ssh/known_hosts"}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := p.DialConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KeyPath != keyPath {
		t.Errorf("key path = %q, want tilde expanded to %q", cfg.KeyPath, keyPath)
	}
	if want := filepath.Join(sshDir, "known_hosts"); cfg.KnownHosts != want {
		t.Errorf("known_hosts path = %q, want tilde expanded to %q", cfg.KnownHosts, want)
	}

	// Bare "~" expands to home itself.
	p, err = ParseParams([]byte(`{"host":"rtb","user":"joe","addr":"/r/federation.sock","key":"~"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err = p.DialConfig(home); err != nil {
		t.Fatal(err)
	}
	if cfg.KeyPath != home {
		t.Errorf(`bare "~" key path = %q, want home %q`, cfg.KeyPath, home)
	}

	// No home to expand against: pass the literal string through rather
	// than guess, so the resulting open failure names it exactly.
	p, err = ParseParams([]byte(`{"host":"rtb","user":"joe","addr":"/r/federation.sock","key":"~/.ssh/foo_key","known_hosts":"/kh"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err = p.DialConfig(""); err != nil {
		t.Fatal(err)
	}
	if cfg.KeyPath != "~/.ssh/foo_key" {
		t.Errorf("no home: key path = %q, want the literal string preserved", cfg.KeyPath)
	}
}

func TestAutoLabel(t *testing.T) {
	if l := autoLabel(`{"host":"rtb","user":"joe"}`); l != "joe@rtb" {
		t.Errorf("autoLabel = %q, want joe@rtb", l)
	}
	if l := autoLabel(""); l != "" {
		t.Errorf("no params: autoLabel = %q, want empty", l)
	}
	// A DIRECT connection (addr only — the shape ParseParams explicitly
	// admits) has no user and no host: the label is the address, never the
	// literal "@" the naive concatenation produced (Joe's tailscale
	// connection wore "@" in the menu, 2026-08-24).
	if l := autoLabel(`{"addr":"100.120.18.84:10010"}`); l != "100.120.18.84:10010" {
		t.Errorf("direct: autoLabel = %q, want the addr", l)
	}
	// Fully empty params: nothing to say — empty, not "@".
	if l := autoLabel(`{}`); l != "" {
		t.Errorf("empty params: autoLabel = %q, want empty", l)
	}
}
