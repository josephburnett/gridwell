// Package dialtest provides a minimal REAL ssh server for tests: public-key
// auth against exactly one authorized key, host-key verification material, and
// direct-streamlocal channel forwarding — everything the ssh plugin's dial path
// needs, nothing else. Shared by the sshdial seam test (in-process) and the
// federation spawn gate (production binaries), so there is one implementation
// of "a throwaway sshd" instead of a hand-rolled copy per smoke.
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

// directStreamLocal is the direct-streamlocal@openssh.com channel-open
// payload (OpenSSH PROTOCOL §2.4): what x/crypto/ssh's Client.Dial("unix",
// path) sends — the only forwarding the federation dial uses since the
// node door became a unix socket (2026-08-26).
type directStreamLocal struct {
	SocketPath string
	Reserved0  string
	Reserved1  uint32
}

// Creds is everything a dialer needs to reach the test sshd: the file paths
// connection params document wants, plus the sshd's address.
type Creds struct {
	Addr           string // the sshd's "host:port"
	KeyPath        string // client private key file
	KnownHostsPath string // known_hosts file trusting the sshd's host key
}

// Server starts a real x/crypto ssh server on a loopback port that accepts
// exactly one freshly-minted client key and forwards direct-streamlocal channels to
// their requested destinations. Key material is written under dir (a
// t.TempDir()); the listener is torn down with the test.
func Server(t *testing.T, dir string) Creds {
	creds, _ := Restartable(t, dir)
	return creds
}

// Handle controls a restartable test sshd: Kill drops the listener AND every
// live ssh session (a listener close alone leaves established sessions
// running — a real outage kills both), Resume rebinds the same address with
// the same host key. This is how a test simulates the tunnel dying — laptop
// sleep, network change, remote sshd restart — the failure the redialer in
// sshdial exists to recover from.
type Handle struct {
	addr string
	conf *ssh.ServerConfig

	mu    sync.Mutex
	ln    net.Listener
	conns map[net.Conn]struct{}
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

// Kill closes the listener and every live connection — the whole sshd is
// gone, established tunnels included. Idempotent.
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

// Resume rebinds the SAME address (so existing creds/known_hosts stay valid)
// and serves again.
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
