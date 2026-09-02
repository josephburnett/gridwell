// Package dial is the transport's dialer: it opens an ssh tunnel to a remote
// host and dials that node's connection door through it with raw gRPC, over
// direct-streamlocal. The far end is the far node's own export, the full
// Gridwell service routed by the qualified ids each request carries, so a
// mount is the whole node: the descent lands on its home and every remote
// plugin is reachable through it. There is no selector and no name
// resolution; routing is by id at every hop.
//
// The whole path — key auth, known_hosts verification, tunnel, gRPC over the
// socket — is testable in-process against a real ssh server; see dialtest and
// internal/server/sshdial_seam_test.go.
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

// Config is one connection's settings. Host is the transport selector: when
// set, the connection bridges over ssh, with its auth and encryption; when
// empty, it dials Addr directly — another node on this machine, where the
// socket's mode is the gate.
type Config struct {
	Host       string // ssh endpoint "host:port"; "" means a direct connection
	User       string // ssh user (ssh only)
	KeyPath    string // private key file (ssh only)
	KnownHosts string // known_hosts file (ssh only; mandatory, no blind trust)
	// Addr is the remote node's connection door: its unix socket path, on
	// the remote host for an ssh bridge — its `federation:` socket,
	// <home>/federation.sock by default — or on this host for a direct
	// connection. Never a TCP address; the door does not exist in that
	// form.
	Addr string
}

// LocalFile is one host-local file a dial plan will open: the server.yaml
// key that names it and the resolved path.
type LocalFile struct {
	Field string // the `connections:` key: "key", "known_hosts"
	Path  string
}

// LocalFiles lists the host-local files Dial opens, in the order it opens
// them. It lives beside the fields it enumerates, so it is the one list: a
// new path field on Config joins it here, where the field is declared, and
// Check — and through Check, the boot gate — picks it up with no second
// copy of what a connection's paths are.
//
// A direct dial opens nothing local. Addr is the far node's socket, and
// whether that socket answers is a network fact, not a config one.
func (c Config) LocalFiles() []LocalFile {
	if c.Host == "" {
		return nil
	}
	return []LocalFile{{Field: "key", Path: c.KeyPath}, {Field: "known_hosts", Path: c.KnownHosts}}
}

// Check reports whether every host-local file this plan needs is there and
// readable. It is the startup gate: a `key:` with a typo in it is a
// misconfiguration the operator must see at `serve`, not a connection that
// comes up quietly dark. It opens each file — existence and permission both,
// since an unreadable key dials no better than an absent one — and reads
// nothing further: what the bytes mean is Dial's business, and whether the
// far node answers is the network's.
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

// redialer is the one owner of "is the ssh session up". Capturing a single
// *ssh.Client in the gRPC dialer closure would leave every reconnect attempt
// tunneling through the same dead session after a laptop sleep, a network
// change, or a remote sshd restart, so the fan-in would retry with perfect
// backoff against a transport that can never recover.
//
// Every consumer of the tunnel dials through dial(), which re-establishes the
// ssh layer on demand: a dead session is dropped and rebuilt on the next
// attempt, single-flight under mu so concurrent callers do not stampede the
// remote sshd.
type redialer struct {
	host    string
	user    string
	auth    []ssh.AuthMethod
	hostKey ssh.HostKeyCallback

	mu     sync.Mutex
	client *ssh.Client // nil means not established, or known dead
}

// dial opens addr on the remote host through the current ssh session,
// establishing or re-establishing the session as needed. A channel-open
// failure drops the whole session and retries once through a fresh one:
// distinguishing "session dead" from "remote endpoint refused" is not worth
// the fragility, and the cost is one extra ssh handshake, bounded by gRPC's
// backoff.
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
	// direct-streamlocal@openssh.com: the remote node's connection door is a unix
	// socket, never a port.
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

// drop forgets a dead session, but only if it is still the current one, since
// a concurrent caller may already have re-established, and closes it so any
// goroutines blocked on it unwind.
func (r *redialer) drop(dead *ssh.Client) {
	r.mu.Lock()
	if r.client == dead {
		r.client = nil
	}
	r.mu.Unlock()
	go dead.Close()
}

// establish returns the current session or dials a fresh one. Holding mu for
// the whole handshake is deliberate: it makes re-establishment single-flight,
// so a burst of failing RPCs produces one ssh handshake, not one each.
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
		// Offer the algorithms known_hosts already trusts for this host
		// first, recomputed each dial because the user may have just fixed
		// the file. Otherwise a server with several host keys can present one
		// the file does not hold, and strict verification reports "key
		// mismatch" on a good host. See hostalgos.go.
		HostKeyAlgorithms: hostKeyAlgorithmsFor(r.hostKey, r.host),
		// Without a timeout a black-holing network — a dropped NAT mapping, a
		// sleeping laptop's dead route — would hang the handshake, and mu,
		// indefinitely.
		Timeout: 10 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("ssh dial %q: %w", r.host, err)
	}
	r.client = c
	return c, nil
}

// close tears down the current session, if any.
func (r *redialer) close() {
	r.mu.Lock()
	c := r.client
	r.client = nil
	r.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// Dial builds the tunnel transport and returns a client of the remote node's
// export plus a closer. The client speaks the remote's qualified ids verbatim,
// and the transit qualification prepends this connection's segment on the way
// back, so chains compose one segment per hop.
//
// Config-shaped failures — an unreadable key, a bad known_hosts — fail here,
// at construction, where they are a misconfiguration to surface; Check has
// already refused the absent ones a boot earlier. The ssh
// session itself is established lazily on first use and re-established after
// any death; see redialer. A connection whose remote is down at construction,
// or whose tunnel dies later, heals by itself the moment the far node is
// reachable again, and until then every RPC fails loudly and the fan-in health
// machinery tells the user.
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

	// A fixed passthrough target: gRPC's resolvers cannot carry a socket
	// path, since they would strip its leading slash, and the dialer never
	// needs it — it opens cfg.Addr on the remote host through the
	// self-healing ssh session, whatever gRPC hands it.
	conn, err := grpc.NewClient("passthrough:///connection",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return rd.dial("unix", cfg.Addr)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// HTTP/2 pings through the tunnel. A session that dies without an
		// error — sleep, NAT expiry — would otherwise leave long-lived
		// streams, the event Subscribe above all, blocked in Recv forever,
		// which presents as tiles silently going stale with no retry and no
		// health notice. The ping timeout turns that into Unavailable, the
		// fan-in retries, and the retry's dial rebuilds the ssh session.
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
		// A connection is one user's tunnel, not a fleet: cap the reconnect
		// backoff well below gRPC's two-minute default so a healed network
		// means a healed connection in seconds.
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  time.Second,
				Multiplier: 1.6,
				Jitter:     0.2,
				MaxDelay:   10 * time.Second,
			},
		}),
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

// dialDirect is the direct transport: a plain gRPC connection to another
// node's connection door on this machine, with the same keepalive and
// healing posture as the tunnel, so a dead peer surfaces as Unavailable rather
// than a silent stale stream and a revived one heals in seconds. There is no
// auth on this path: the socket's 0600 mode is the gate, admitting the same
// uid only. Across machines the ssh bridge is the one authenticated
// transport.
func dialDirect(addr string) (namespace.Namespace, func(), error) {
	conn, err := grpc.NewClient("unix:"+addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  time.Second,
				Multiplier: 1.6,
				Jitter:     0.2,
				MaxDelay:   10 * time.Second,
			},
		}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("direct dial %s: %w", addr, err)
	}
	return namespace.FromClient(gridwellv1.NewGridwellClient(conn)), func() { _ = conn.Close() }, nil
}
