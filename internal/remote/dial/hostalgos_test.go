package dial

// The knownhosts key-mismatch class (Joe's laptop, 2026-08-23): the file
// holds the host's ed25519 key (what `ssh` saved on first contact), but
// the handshake negotiated a different host-key algorithm, so strict
// verification reported "key mismatch" on a perfectly good host.
// OpenSSH offers the KNOWN algorithms first; so must we.

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestHostKeyAlgorithmsFromKnownHosts(t *testing.T) {
	// A REAL ed25519 entry for the host (what `ssh` saves).
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	khLine := "example.com " + string(ssh.MarshalAuthorizedKey(sshPub))
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(khLine), 0o600); err != nil {
		t.Fatal(err)
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		t.Fatal(err)
	}
	got := hostKeyAlgorithmsFor(cb, "example.com:22")
	if len(got) == 0 || got[0] != "ssh-ed25519" {
		t.Fatalf("algorithms for a known ed25519 host = %v, want ssh-ed25519 first — "+
			"without this the handshake negotiates a key type the file cannot verify (key mismatch)", got)
	}
	// An unknown host constrains nothing: the default negotiation runs
	// and the unknown-host error surfaces normally.
	if got := hostKeyAlgorithmsFor(cb, "stranger.example:22"); got != nil {
		t.Fatalf("unknown host must not constrain algorithms, got %v", got)
	}
}
