package sshdial_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/localdb"
	"github.com/josephburnett/gridwell/internal/plugin/sshdial"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/shellsvc"
	"github.com/josephburnett/gridwell/internal/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/store"
)

// This is the ssh plugin's REAL transport seam, in-process: a genuine
// x/crypto/ssh server (public-key auth, host-key verification against a
// known_hosts file, direct-tcpip channel forwarding) in front of a genuine
// `gridwell serve` node handler (h2c + node export + in-process localdb).
// sshdial.Dial crosses every layer the production binary crosses except the
// network itself. The old gap: the proxy was tested against an in-process
// fake and the tunnel against nothing — both sides tested, seam never.

// remoteNode stands up the "remote gridwell serve": one localdb (label
// "personal") behind NodeHandler on a real listener. Returns its address and
// a direct client for ground-truth assertions.
func remoteNode(t *testing.T) (string, gridwellv1.GridwellClient) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	direct, closer, err := plugin.ServeInProcess(localdb.New(st, shellsvc.NewManager(shellsvctest.New())))
	if err != nil {
		t.Fatalf("serve localdb: %v", err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register("ur1", "localdb", direct, nil)
	reg.SetLabel("ur1", "personal")
	srv := server.New(reg, server.Config{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	httpSrv := &http.Server{Handler: srv.NodeHandler()}
	go httpSrv.Serve(ln)
	t.Cleanup(func() { httpSrv.Close() })
	return ln.Addr().String(), direct
}

// directTCPIP is the SSH direct-tcpip channel-open payload (RFC 4254 §7.2).
type directTCPIP struct {
	DestHost string
	DestPort uint32
	SrcHost  string
	SrcPort  uint32
}

// sshServer runs a minimal real sshd: public-key auth accepting exactly
// clientPub, and direct-tcpip channels piped to their requested destination.
// Returns its address and the host public key (for the known_hosts file).
func sshServer(t *testing.T, clientPub ssh.PublicKey) (string, ssh.PublicKey) {
	t.Helper()
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
	go func() {
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
	}()
	return ln.Addr().String(), hostSigner.PublicKey()
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

// dialThroughSSH assembles the full topology and returns a scoped client.
func dialThroughSSH(t *testing.T, remotePlugin string) (gridwellv1.GridwellClient, gridwellv1.GridwellClient, error) {
	t.Helper()
	nodeAddr, direct := remoteNode(t)

	// Chicken-and-egg: the client pubkey is needed by the sshd, the sshd
	// address by known_hosts. Generate the client key first with a throwaway
	// host, then regenerate known_hosts — simpler: create sshd with a
	// deferred pubkey check.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("client key: %v", err)
	}
	clientPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("client pub: %v", err)
	}
	sshAddr, hostPub := sshServer(t, clientPub)

	dir := t.TempDir()
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	khPath := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(khPath, []byte(knownhosts.Line([]string{sshAddr}, hostPub)+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	client, closer, err := sshdial.Dial(sshdial.Config{
		Host:         sshAddr,
		User:         "joe",
		KeyPath:      keyPath,
		KnownHosts:   khPath,
		Addr:         nodeAddr,
		RemotePlugin: remotePlugin,
	})
	if err != nil {
		return nil, direct, err
	}
	t.Cleanup(closer)
	return client, direct, nil
}

func TestDialMountsRemotePluginThroughRealSSH(t *testing.T) {
	c, direct, err := dialThroughSSH(t, "personal")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	ctx := context.Background()

	want, err := direct.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("direct Info: %v", err)
	}
	got, err := c.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("tunneled Info: %v", err)
	}
	if got.Kind != want.Kind || got.RootGridId != want.RootGridId {
		t.Fatalf("tunneled Info = %+v, want the remote plugin's %+v", got, want)
	}

	// Write through the tunnel, read directly: the mount is the same plugin.
	created, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: got.RootGridId,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 2, H: 2},
		Data:   []byte("# over ssh"),
	})
	if err != nil {
		t.Fatalf("CreateTile through tunnel: %v", err)
	}
	body, err := direct.GetTileContent(ctx, &gridwellv1.GetTileContentRequest{TileId: created.Tile.Id})
	if err != nil {
		t.Fatalf("direct read: %v", err)
	}
	if string(body.Data) != "# over ssh" {
		t.Errorf("content = %q, want %q", body.Data, "# over ssh")
	}
}

func TestDialAutoSelectsTheOnlyRemotePlugin(t *testing.T) {
	c, _, err := dialThroughSSH(t, "") // no remote_plugin: exactly one exists
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if _, err := c.Info(context.Background(), &gridwellv1.InfoRequest{}); err != nil {
		t.Fatalf("Info after auto-select: %v", err)
	}
}

func TestDialUnknownRemotePluginEnumeratesOptions(t *testing.T) {
	_, _, err := dialThroughSSH(t, "nope")
	if err == nil {
		t.Fatalf("Dial with unknown remote_plugin succeeded")
	}
	if !strings.Contains(err.Error(), "personal") {
		t.Errorf("error %q should enumerate the remote's plugins", err)
	}
}

func TestFromPluginConfigNamesEveryMissingKey(t *testing.T) {
	_, err := sshdial.FromPluginConfig(map[string]string{"host": "h:22"})
	if err == nil {
		t.Fatalf("want error for missing keys")
	}
	// The exact missing-key list: every absent key named, the provided one not.
	msg := err.Error()
	list := msg[strings.Index(msg, ": ")+2 : strings.Index(msg, " (")]
	if list != "user, key, known_hosts, addr" {
		t.Errorf("missing keys = %q, want %q", list, "user, key, known_hosts, addr")
	}
	c, err := sshdial.FromPluginConfig(map[string]string{
		"host": "h:22", "user": "u", "key": "/k", "known_hosts": "/kh", "addr": "127.0.0.1:8080",
	})
	if err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}
	if c.RemotePlugin != "" {
		t.Errorf("RemotePlugin = %q, want empty (optional)", c.RemotePlugin)
	}
}
