// Package dial opens an ssh tunnel to a remote host and dials that node's
// connection door through it with raw gRPC over direct-streamlocal. The far
// end is the far node's own export, routed by the qualified ids each request
// carries, so a mount is the whole node and routing is by id at every hop.
package dial

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// Config's Host is the transport selector: set, the connection bridges over
// ssh; empty, it dials Addr directly on this machine, where the socket's mode
// is the gate.
type Config struct {
	Host       string // ssh endpoint "host:port"; "" means a direct connection
	User       string // ssh user (ssh only)
	KeyPath    string // private key file (ssh only)
	KnownHosts string // known_hosts file (ssh only; mandatory, no blind trust)
	// Addr is the far node's `federation:` socket path, on the remote host
	// for an ssh bridge or on this one for a direct dial. Never a TCP
	// address; the door has no such form.
	Addr string
}

// LocalFile is one host-local file a dial plan opens, by its server.yaml key.
type LocalFile struct {
	Field string // the `connections:` key: "key", "known_hosts"
	Path  string
}

// LocalFiles lives beside the fields it enumerates, so a new path field on
// Config joins it here and Check picks it up with no second copy. A direct dial
// opens nothing local: whether Addr answers is a network fact, not a config one.
func (c Config) LocalFiles() []LocalFile {
	if c.Host == "" {
		return nil
	}
	return []LocalFile{{Field: "key", Path: c.KeyPath}, {Field: "known_hosts", Path: c.KnownHosts}}
}

// Check is the startup gate, so a `key:` with a typo in it reaches the
// operator at `serve` instead of coming up as a quietly dark connection.
func (c Config) Check() error {
	for _, f := range c.LocalFiles() {
		fh, err := os.Open(f.Path)
		if err != nil {
			// os.Open's error already carries the exact path.
			return fmt.Errorf("%s: %w", f.Field, err)
		}
		_ = fh.Close()
	}
	return nil
}

// redialer is the one owner of "is the ssh session up". A single *ssh.Client
// captured in the gRPC dialer closure would tunnel every reconnect attempt
// through the same dead session after a laptop sleep. dial drops a dead session
// and rebuilds it, single-flight under mu so callers do not stampede sshd.
type redialer struct {
	host    string
	user    string
	auth    []ssh.AuthMethod
	hostKey ssh.HostKeyCallback

	mu     sync.Mutex
	client *ssh.Client // nil means not established, or known dead
}

// dial drops the whole session on a channel-open failure and retries once:
// telling a dead session from a refused endpoint is not worth the fragility,
// and the cost is one extra handshake.
func (r *redialer) dial(_ string, addr string) (net.Conn, error) {
	if c := r.current(); c != nil {
		conn, err := c.Dial("unix", addr)
		if err == nil {
			return conn, nil
		}
		r.drop(c)
	}
	c, err := r.establish()
	if err != nil {
		return nil, err
	}
	conn, err := c.Dial("unix", addr)
	if err != nil {
		r.drop(c)
		return nil, err
	}
	return conn, nil
}

func (r *redialer) current() *ssh.Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.client
}

// drop forgets a dead session only if it is still the current one, a
// concurrent caller may already have re-established.
func (r *redialer) drop(dead *ssh.Client) {
	r.mu.Lock()
	if r.client == dead {
		r.client = nil
	}
	r.mu.Unlock()
	go dead.Close()
}

// establish holds mu across the handshake, so a burst of failing RPCs costs
// one.
func (r *redialer) establish() (*ssh.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != nil {
		return r.client, nil
	}
	c, err := ssh.Dial("tcp", r.host, &ssh.ClientConfig{
		User:            r.user,
		Auth:            r.auth,
		HostKeyCallback: r.hostKey,
		// Recomputed each dial in case the user just fixed the file. See
		// hostalgos.go.
		HostKeyAlgorithms: hostKeyAlgorithmsFor(r.hostKey, r.host),
		// Without it a black-holing network hangs the handshake, and mu with
		// it, indefinitely.
		Timeout: 10 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("ssh dial %q: %w", r.host, err)
	}
	r.client = c
	return c, nil
}

func (r *redialer) close() {
	r.mu.Lock()
	c := r.client
	r.client = nil
	r.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// grpcDialOptions is the posture both dials wear, so the ssh bridge and the
// direct socket cannot drift apart. Each caller adds only what its transport
// needs.
func grpcDialOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// HTTP/2 pings. A far side that dies without an error would leave
		// long-lived streams blocked in Recv forever; the timeout makes that
		// Unavailable and the fan-in's retry rebuilds.
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
		// One user's connection, so cap the backoff well below gRPC's
		// two-minute default and a healed network heals in seconds.
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  time.Second,
				Multiplier: 1.6,
				Jitter:     0.2,
				MaxDelay:   10 * time.Second,
			},
		}),
	}
}

// Dial returns a client of the remote node's export plus a closer. The client
// speaks the remote's qualified ids verbatim, and the transit qualification
// prepends this connection's segment on the way back, so chains compose one
// segment per hop. The ssh session comes up lazily and re-establishes after any
// death, so until the far node is reachable every RPC fails loudly.
func Dial(cfg Config) (client namespace.Namespace, closer func(), err error) {
	if cfg.Host == "" {
		return dialDirect(cfg.Addr)
	}
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
	rd := &redialer{
		host:    cfg.Host,
		user:    cfg.User,
		auth:    []ssh.AuthMethod{ssh.PublicKeys(signer)},
		hostKey: hostKey,
	}

	// A fixed passthrough target: gRPC's resolvers would strip the leading
	// slash off a socket path, and the dialer opens cfg.Addr regardless.
	conn, err := grpc.NewClient("passthrough:///connection",
		append(grpcDialOptions(), grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return rd.dial("unix", cfg.Addr)
		}))...,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("grpc over tunnel: %w", err)
	}

	closer = func() {
		_ = conn.Close()
		rd.close()
	}
	return namespace.FromClient(gridwellv1.NewGridwellClient(conn)), closer, nil
}

// dialDirect is a plain gRPC connection to another node's door on this
// machine. There is no auth: the socket's 0600 mode is the gate, admitting the
// same uid only. Across machines the ssh bridge is the one authenticated
// transport.
func dialDirect(addr string) (namespace.Namespace, func(), error) {
	conn, err := grpc.NewClient("unix:"+addr, grpcDialOptions()...)
	if err != nil {
		return nil, nil, fmt.Errorf("direct dial %s: %w", addr, err)
	}
	return namespace.FromClient(gridwellv1.NewGridwellClient(conn)), func() { _ = conn.Close() }, nil
}
