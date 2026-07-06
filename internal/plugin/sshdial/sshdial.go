// Package sshdial is the ssh plugin's transport: it opens an SSH tunnel to a
// remote host and dials the remote gridwell node's one HTTP/h2c port through
// it with raw gRPC. The far end is the remote's node export — the full
// Gridwell service, routed by the qualified ids each request carries — so the
// mount is the WHOLE NODE: its node grid (the plugin-list landing page) is
// the root the descent lands on, and every remote plugin is reachable through
// it. No selector, no name resolution — routing is by id at every hop.
//
// Extracted from cmd/plugin/ssh so the whole path — key auth, known_hosts
// verification, tunnel, gRPC-over-h2c — is testable in-process against a real
// ssh server (sshdial_test.go); the binary's main is a thin caller.
package sshdial

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Config is the ssh plugin's connection settings.
type Config struct {
	Host       string // SSH endpoint "host:port"
	User       string // SSH user
	KeyPath    string // private key file
	KnownHosts string // known_hosts file (mandatory — no blind trust)
	Addr       string // the remote node's HTTP/h2c address AS SEEN ON THE REMOTE HOST (e.g. its `bind:`, 127.0.0.1:8080)
}

// FromPluginConfig validates the plugin's server.yaml config map. Every
// missing key is named in one error so a misconfiguration reads as a recipe,
// not a scavenger hunt. A leftover remote_plugin key (the pre-node-mount
// design selected one remote plugin) is reported as obsolete rather than
// silently ignored — the mount is the whole node now.
func FromPluginConfig(cfg map[string]string) (Config, error) {
	c := Config{
		Host:       cfg["host"],
		User:       cfg["user"],
		KeyPath:    cfg["key"],
		KnownHosts: cfg["known_hosts"],
		Addr:       cfg["addr"],
	}
	var missing []string
	for _, kv := range []struct{ k, v string }{
		{"host", c.Host}, {"user", c.User}, {"key", c.KeyPath},
		{"known_hosts", c.KnownHosts}, {"addr", c.Addr},
	} {
		if kv.v == "" {
			missing = append(missing, kv.k)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("ssh plugin config missing required keys: %s", strings.Join(missing, ", "))
	}
	if _, ok := cfg["remote_plugin"]; ok {
		fmt.Fprintln(os.Stderr, "gridwell-ssh: config key remote_plugin is obsolete and ignored — the mount is the whole remote node; descend into it to reach every remote plugin")
	}
	return c, nil
}

// Dial opens the tunnel and returns a client of the remote node's export plus
// a closer. The client speaks the remote's qualified ids verbatim; the host
// server's transit qualification prepends this plugin's uuid on the way back
// to the local client, so chains compose one segment per hop.
func Dial(cfg Config) (gridwellv1.GridwellClient, func(), error) {
	keyBytes, err := os.ReadFile(cfg.KeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read key %q: %w", cfg.KeyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse key: %w", err)
	}
	hostKey, err := knownhosts.New(cfg.KnownHosts)
	if err != nil {
		return nil, nil, fmt.Errorf("load known_hosts %q: %w", cfg.KnownHosts, err)
	}
	sshClient, err := ssh.Dial("tcp", cfg.Host, &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKey,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("ssh dial %q: %w", cfg.Host, err)
	}

	conn, err := grpc.NewClient(cfg.Addr,
		grpc.WithContextDialer(func(_ context.Context, a string) (net.Conn, error) {
			// The dialer ignores grpc's notion of the address and opens addr
			// on the remote host through the SSH connection.
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
