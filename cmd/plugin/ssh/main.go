// gridwell-ssh is the remote-gateway plugin binary. It dials a remote host over
// SSH, tunnels to that host's Gridwell gRPC endpoint, and serves a transparent
// proxy (internal/plugin/proxy) of the remote node's entire service — so a
// remote plugin's wells, tiles, live shell PTYs (OpenShell) and session blob
// all reach the local server over the same Gridwell interface as a local
// plugin. "Remote" is just a transport: the proxy does the work.
//
// Config keys:
//
//	host: SSH endpoint "host:port" (e.g. "example.com:22")
//	user: SSH user
//	key:  path to the private key file
//	addr: the remote node's gRPC listen address, as seen on the remote host
//	      (e.g. "127.0.0.1:9090") — dialed through the tunnel
package main

import (
	"context"
	"fmt"
	"net"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin/guest"
	"github.com/josephburnett/gridwell/internal/plugin/pluginmeta"
	"github.com/josephburnett/gridwell/internal/plugin/proxy"
)

func main() {
	cfg := guest.Config()
	// The ssh plugin is a transparent proxy, but it still owns a local DB like
	// every plugin — it records its durable identity (and may later stash keys
	// etc.) there. Verify id+kind against that DB before dialing out.
	if dbPath := cfg["db_file"]; dbPath != "" {
		if _, err := pluginmeta.Ensure(dbPath, cfg["uuid"], cfg["kind"]); err != nil {
			fmt.Fprintf(os.Stderr, "gridwell-ssh: %v\n", err)
			os.Exit(1)
		}
	}
	client, closer, err := dialRemote(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-ssh: %v\n", err)
		os.Exit(1)
	}
	defer closer()
	guest.Serve(proxy.New(client))
}

// dialRemote opens the SSH tunnel and returns a Gridwell client to the remote
// node's gRPC endpoint reached through it, plus a closer.
func dialRemote(cfg map[string]string) (gridwellv1.GridwellClient, func(), error) {
	host, user, keyPath, addr := cfg["host"], cfg["user"], cfg["key"], cfg["addr"]
	if host == "" || user == "" || keyPath == "" || addr == "" {
		return nil, nil, fmt.Errorf("ssh plugin requires host, user, key, addr config keys")
	}
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read key %q: %w", keyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse key: %w", err)
	}
	hostKey, err := hostKeyCallback(cfg["known_hosts"])
	if err != nil {
		return nil, nil, err
	}
	sshCfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKey,
	}
	sshClient, err := ssh.Dial("tcp", host, sshCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("ssh dial %q: %w", host, err)
	}

	// gRPC over the tunnel: the dialer ignores grpc's notion of the address and
	// opens addr on the remote host through the SSH connection.
	conn, err := grpc.NewClient(addr,
		grpc.WithContextDialer(func(_ context.Context, a string) (net.Conn, error) {
			return sshClient.Dial("tcp", a)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		sshClient.Close()
		return nil, nil, fmt.Errorf("grpc over tunnel: %w", err)
	}
	closer := func() {
		_ = conn.Close()
		_ = sshClient.Close()
	}
	return gridwellv1.NewGridwellClient(conn), closer, nil
}

// hostKeyCallback verifies the remote host key against a known_hosts file.
// Refusing to trust an unverified host is the secure default — there is no
// "accept anything" path, so a missing/empty known_hosts is a config error.
func hostKeyCallback(path string) (ssh.HostKeyCallback, error) {
	if path == "" {
		return nil, fmt.Errorf("ssh plugin: known_hosts config key is required (won't trust a host blindly)")
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("ssh plugin: load known_hosts %q: %w", path, err)
	}
	return cb, nil
}
