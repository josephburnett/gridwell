package dial

// The black-hole seam: a host that completes the TCP connect and then says
// nothing, which is what a wedged sshd or a network eating packets after the
// SYN looks like from here. establish holds mu across the handshake, so an
// unbounded handshake is not one slow RPC but a connection stuck forever.

import (
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// blackHole accepts every connection and never writes a byte, so the ssh
// version exchange has nothing to read and no error to read either.
func blackHole(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				<-done
				c.Close()
			}()
		}
	}()
	t.Cleanup(func() {
		close(done)
		ln.Close()
	})
	return ln.Addr().String()
}

// establishWithin runs one establish off the test goroutine so a hang is a
// failure at the bound instead of a hung test binary.
func establishWithin(t *testing.T, r *redialer, bound time.Duration, what string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := r.establish()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("%s: establish succeeded against a black hole", what)
		}
	case <-time.After(bound):
		t.Fatalf("%s: establish still parked %v after the handshake began, "+
			"want an error within sshHandshakeTimeout", what, bound)
	}
}

func TestABlackHoleHandshakeFailsWithinTheTimeout(t *testing.T) {
	was := sshHandshakeTimeout
	t.Cleanup(func() { sshHandshakeTimeout = was })
	sshHandshakeTimeout = 200 * time.Millisecond
	bound := 5 * sshHandshakeTimeout

	r := &redialer{
		host: blackHole(t),
		user: "nobody",
		// Reached only if the far side speaks, which a black hole never does.
		hostKey: func(string, net.Addr, ssh.PublicKey) error {
			return errors.New("host key checked against a silent peer")
		},
	}
	establishWithin(t, r, bound, "first")
	// mu is released on the failure path, so a second caller is not inheriting
	// the first one's wait.
	establishWithin(t, r, bound, "second")
}

// The default is what the node runs, and a handshake is the one place a user
// waits on a stranger's network.
func TestTheHandshakeTimeoutDefaultIsTenSeconds(t *testing.T) {
	if sshHandshakeTimeout != 10*time.Second {
		t.Fatalf("sshHandshakeTimeout = %v, want 10s", sshHandshakeTimeout)
	}
}
