// Package sshdial is the ssh plugin's transport: it opens an SSH tunnel to a
// remote host, dials the remote gridwell node's one HTTP/h2c port through it
// with raw gRPC, resolves WHICH remote plugin to mount, and returns a client
// whose every call carries the node-export selector
// (plugin.NodeExportHeader) — so the far end answers with that plugin's own
// service verbatim and the proxy on this side stays a dumb pipe.
//
// Extracted from cmd/plugin/ssh so the whole path — key auth, known_hosts
// verification, tunnel, gRPC-over-h2c, plugin resolution — is testable
// in-process against a real ssh server (sshdial_test.go); the binary's main
// is a thin caller.
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
	"google.golang.org/grpc/metadata"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// Config is the ssh plugin's connection settings.
type Config struct {
	Host       string // SSH endpoint "host:port"
	User       string // SSH user
	KeyPath    string // private key file
	KnownHosts string // known_hosts file (mandatory — no blind trust)
	Addr       string // the remote node's HTTP/h2c address AS SEEN ON THE REMOTE HOST (e.g. its `bind:`, 127.0.0.1:8080)
	// RemotePlugin selects which of the remote node's plugins to mount, by
	// config name or uuid. Empty is allowed only when the remote has exactly
	// one plugin.
	RemotePlugin string
}

// FromPluginConfig validates the plugin's server.yaml config map. Every
// missing key is named in one error so a misconfiguration reads as a recipe,
// not a scavenger hunt.
func FromPluginConfig(cfg map[string]string) (Config, error) {
	c := Config{
		Host:         cfg["host"],
		User:         cfg["user"],
		KeyPath:      cfg["key"],
		KnownHosts:   cfg["known_hosts"],
		Addr:         cfg["addr"],
		RemotePlugin: cfg["remote_plugin"],
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
		return Config{}, fmt.Errorf("ssh plugin config missing required keys: %s (remote_plugin is optional when the remote has exactly one plugin)", strings.Join(missing, ", "))
	}
	return c, nil
}

// Dial opens the tunnel, connects, resolves the remote plugin, and returns a
// scoped client plus a closer. The returned client's calls all carry the
// node-export selector for the resolved plugin.
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

	// One conn for both phases: the selector starts empty (resolution calls
	// go to the remote's client-facing front door, which serves ListPlugins),
	// then is set so every subsequent call routes to the node export. Startup
	// is single-threaded, so the write happens-before any scoped call.
	selected := new(string)
	conn, err := grpc.NewClient(cfg.Addr,
		grpc.WithContextDialer(func(_ context.Context, a string) (net.Conn, error) {
			// The dialer ignores grpc's notion of the address and opens addr
			// on the remote host through the SSH connection.
			return sshClient.Dial("tcp", a)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			return invoker(scope(ctx, *selected), method, req, reply, cc, opts...)
		}),
		grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			return streamer(scope(ctx, *selected), desc, cc, method, opts...)
		}),
	)
	if err != nil {
		sshClient.Close()
		return nil, nil, fmt.Errorf("grpc over tunnel: %w", err)
	}
	closer := func() {
		_ = conn.Close()
		_ = sshClient.Close()
	}

	client := gridwellv1.NewGridwellClient(conn)
	uuid, err := resolveRemotePlugin(client, cfg.RemotePlugin)
	if err != nil {
		closer()
		return nil, nil, err
	}
	*selected = uuid
	return client, closer, nil
}

// scope stamps the node-export selector when one is set; resolution-phase
// calls (selector still empty) pass through to the remote's front door.
func scope(ctx context.Context, selected string) context.Context {
	if selected == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, plugin.NodeExportHeader, selected)
}

// resolveRemotePlugin turns the configured remote_plugin (name or uuid, or
// empty when unambiguous) into the remote plugin's uuid, via the remote
// front door's ListPlugins. Failures enumerate what IS there, so fixing the
// config never needs a remote shell.
func resolveRemotePlugin(c gridwellv1.GridwellClient, want string) (string, error) {
	resp, err := c.ListPlugins(context.Background(), &gridwellv1.ListPluginsRequest{})
	if err != nil {
		return "", fmt.Errorf("list remote plugins: %w", err)
	}
	var names []string
	for _, p := range resp.Plugins {
		names = append(names, fmt.Sprintf("%s (%s)", p.Label, p.Uuid))
		if want != "" && (p.Uuid == want || p.Label == want) {
			return p.Uuid, nil
		}
	}
	if want != "" {
		return "", fmt.Errorf("remote has no plugin %q; it has: %s", want, strings.Join(names, ", "))
	}
	if len(resp.Plugins) == 1 {
		return resp.Plugins[0].Uuid, nil
	}
	return "", fmt.Errorf("remote has %d plugins — set remote_plugin to one of: %s", len(resp.Plugins), strings.Join(names, ", "))
}
