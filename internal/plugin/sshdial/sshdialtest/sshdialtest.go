// Package sshdialtest provides a minimal REAL ssh server for tests: public-key
// auth against exactly one authorized key, host-key verification material, and
// direct-tcpip channel forwarding — everything the ssh plugin's dial path
// needs, nothing else. Shared by the sshdial seam test (in-process) and the
// federation spawn gate (production binaries), so there is one implementation
// of "a throwaway sshd" instead of a hand-rolled copy per smoke.
package sshdialtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// directTCPIP is the SSH direct-tcpip channel-open payload (RFC 4254 §7.2).
type directTCPIP struct {
	DestHost string
	DestPort uint32
	SrcHost  string
	SrcPort  uint32
}

// Creds is everything a dialer needs to reach the test sshd: the file paths
// FromPluginConfig-style config wants, plus the sshd's address.
type Creds struct {
	Addr           string // the sshd's "host:port"
	KeyPath        string // client private key file
	KnownHostsPath string // known_hosts file trusting the sshd's host key
}

// Server starts a real x/crypto ssh server on a loopback port that accepts
// exactly one freshly-minted client key and forwards direct-tcpip channels to
// their requested destinations. Key material is written under dir (a
// t.TempDir()); the listener is torn down with the test.
func Server(t *testing.T, dir string) Creds {
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
	t.Cleanup(func() { ln.Close() })
	go serve(ln, conf)

	khPath := filepath.Join(dir, "known_hosts")
	line := knownhosts.Line([]string{ln.Addr().String()}, hostSigner.PublicKey()) + "\n"
	if err := os.WriteFile(khPath, []byte(line), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	return Creds{Addr: ln.Addr().String(), KeyPath: keyPath, KnownHostsPath: khPath}
}

func serve(ln net.Listener, conf *ssh.ServerConfig) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			sc, chans, reqs, err := ssh.NewServerConn(c, conf)
			if err != nil {
				return
			}
			defer sc.Close()
			go ssh.DiscardRequests(reqs)
			for newChan := range chans {
				if newChan.ChannelType() != "direct-tcpip" {
					newChan.Reject(ssh.UnknownChannelType, "only direct-tcpip")
					continue
				}
				var msg directTCPIP
				if err := ssh.Unmarshal(newChan.ExtraData(), &msg); err != nil {
					newChan.Reject(ssh.ConnectionFailed, "bad payload")
					continue
				}
				ch, chReqs, err := newChan.Accept()
				if err != nil {
					continue
				}
				go ssh.DiscardRequests(chReqs)
				go pipeTo(ch, net.JoinHostPort(msg.DestHost, strconv.Itoa(int(msg.DestPort))))
			}
		}()
	}
}

func pipeTo(ch ssh.Channel, addr string) {
	defer ch.Close()
	target, err := net.Dial("tcp", addr)
	if err != nil {
		return
	}
	defer target.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(target, ch); done <- struct{}{} }()
	go func() { io.Copy(ch, target); done <- struct{}{} }()
	<-done
}
