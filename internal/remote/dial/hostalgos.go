package dial

// The knownhosts algorithm bridge: strict verification fails with "key
// mismatch" when the handshake negotiates a host-key TYPE the file
// doesn't hold — even though the host is known and honest (the file has
// its ed25519 key; the server also has an ecdsa key; the default
// negotiation picks ecdsa). OpenSSH avoids this by offering the KNOWN
// algorithms first; hostKeyAlgorithmsFor recovers them from the
// knownhosts callback so the ClientConfig can do the same.

import (
	"crypto/ed25519"
	"errors"
	"net"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// hostKeyAlgorithmsFor returns the host-key algorithms known_hosts
// already trusts for host ("host:port"), by probing the callback with a
// throwaway key and reading the KeyError's Want list. nil for an unknown
// host — the default negotiation runs and the unknown-host error
// surfaces normally.
func hostKeyAlgorithmsFor(cb ssh.HostKeyCallback, host string) []string {
	// A throwaway key of a real type; if the file happens to hold an
	// ed25519 key whose bytes match this fresh one the universe has
	// bigger problems.
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil
	}
	// The callback wants the same "host:port" form the ssh library
	// passes during a real handshake.
	probeErr := cb(host, &net.TCPAddr{IP: net.IPv4zero, Port: 22}, signer.PublicKey())
	var ke *knownhosts.KeyError
	if !errors.As(probeErr, &ke) || len(ke.Want) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var algos []string
	for _, kk := range ke.Want {
		t := kk.Key.Type()
		if !seen[t] {
			seen[t] = true
			algos = append(algos, t)
		}
	}
	return algos
}
