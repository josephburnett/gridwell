// Package dialtest provides a real ssh server for tests: public-key auth against
// exactly one authorized key, host-key verification material, and
// direct-streamlocal channel forwarding. The sshdial seam test and the
// connections spawn gate share it rather than hand-rolling an sshd each.
package dialtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// directStreamLocal is the direct-streamlocal@openssh.com channel-open payload,
// what x/crypto/ssh's Client.Dial("unix", path) sends.
type directStreamLocal struct {
	SocketPath string
	Reserved0  string
	Reserved1  uint32
}

// Creds is everything a dialer needs to reach the test sshd.
type Creds struct {
	Addr           string // the sshd's "host:port"
	KeyPath        string // client private key file
	KnownHostsPath string // known_hosts file trusting the sshd's host key
}

// Server starts a real x/crypto ssh server on a loopback port that accepts one
// freshly-minted client key and forwards direct-streamlocal channels. Key
// material goes under dir, and the listener is torn down with the test.
func Server(t *testing.T, dir string) Creds {
	creds, _ := Restartable(t, dir)
	return creds
}

// Handle controls a restartable test sshd. Kill drops the listener and every
// live ssh session, since closing the listener alone leaves established sessions
// running and a real outage kills both. That is how a test simulates the tunnel
// dying, the failure the redialer in internal/connection/dial recovers from.
type Handle struct {
	addr string
	conf *ssh.ServerConfig

	mu     sync.Mutex
	ln     net.Listener
	conns  map[net.Conn]struct{}
	silent bool
}

// Restartable is Server with a Handle for killing and resuming the sshd.
func Restartable(t *testing.T, dir string) (Creds, *Handle) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("client key: %v", err)
	}
	clientPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("client pub: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal client key: %v", err)
	}
	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write client key: %v", err)
	}

	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}
	conf := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) != string(clientPub.Marshal()) {
				return nil, io.EOF // any error rejects
			}
			return &ssh.Permissions{}, nil
		},
	}
	conf.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("sshd listen: %v", err)
	}
	h := &Handle{addr: ln.Addr().String(), conf: conf, ln: ln, conns: map[net.Conn]struct{}{}}
	t.Cleanup(h.Kill)
	go h.serve(ln)

	khPath := filepath.Join(dir, "known_hosts")
	line := knownhosts.Line([]string{h.addr}, hostSigner.PublicKey()) + "\n"
	if err := os.WriteFile(khPath, []byte(line), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	return Creds{Addr: h.addr, KeyPath: keyPath, KnownHostsPath: khPath}, h
}

// Kill closes the listener and every live connection. Idempotent.
func (h *Handle) Kill() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ln != nil {
		_ = h.ln.Close()
		h.ln = nil
	}
	for c := range h.conns {
		_ = c.Close()
	}
	h.conns = map[net.Conn]struct{}{}
}

// Resume rebinds the same address, keeping existing creds valid, and serves again.
func (h *Handle) Resume(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ln != nil {
		return
	}
	ln, err := net.Listen("tcp", h.addr)
	if err != nil {
		t.Fatalf("sshd resume on %s: %v", h.addr, err)
	}
	h.ln = ln
	go h.serve(ln)
}

// Silence leaves every channel open from now on unanswered, neither accepted
// nor refused: what a dialer sees when the session died under its open.
func (h *Handle) Silence() {
	h.mu.Lock()
	h.silent = true
	h.mu.Unlock()
}

func (h *Handle) isSilent() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.silent
}

func (h *Handle) track(c net.Conn) {
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
}

func (h *Handle) untrack(c net.Conn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
}

func (h *Handle) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		h.track(c)
		go func() {
			defer h.untrack(c)
			sc, chans, reqs, err := ssh.NewServerConn(c, h.conf)
			if err != nil {
				return
			}
			defer sc.Close()
			go ssh.DiscardRequests(reqs)
			for newChan := range chans {
				if h.isSilent() {
					continue
				}
				if newChan.ChannelType() != "direct-streamlocal@openssh.com" {
					newChan.Reject(ssh.UnknownChannelType, "only direct-streamlocal@openssh.com")
					continue
				}
				var msg directStreamLocal
				if err := ssh.Unmarshal(newChan.ExtraData(), &msg); err != nil {
					newChan.Reject(ssh.ConnectionFailed, "bad payload")
					continue
				}
				ch, chReqs, err := newChan.Accept()
				if err != nil {
					continue
				}
				go ssh.DiscardRequests(chReqs)
				go pipeTo(ch, msg.SocketPath)
			}
		}()
	}
}

func pipeTo(ch ssh.Channel, socketPath string) {
	defer ch.Close()
	target, err := net.Dial("unix", socketPath)
	if err != nil {
		return
	}
	defer target.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(target, ch); done <- struct{}{} }()
	go func() { io.Copy(ch, target); done <- struct{}{} }()
	<-done
}
