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
)

// Config is the remote plugin's connection settings. Host is the
// TRANSPORT SELECTOR (owner decision 2026-08-16): set, the connection
// bridges over ssh (auth + encryption); empty, it dials Addr DIRECTLY —
// another node on this machine or across the tailnet, where the network
// itself is the trust boundary.
type Config struct {
	Host       string // SSH endpoint "host:port"; "" = direct connection
	User       string // SSH user (ssh only)
	KeyPath    string // private key file (ssh only)
	KnownHosts string // known_hosts file (ssh only; mandatory — no blind trust)
	// Addr is the remote node's federation door: its UNIX SOCKET PATH —
	// on the remote host for an ssh bridge (its federation.socket,
	// <home>/federation.sock by default), on this host for a direct
	// connection (another node on this machine). Never a TCP address:
	// the door does not exist in that form (owner decision 2026-08-26).
	Addr string
}

// redialer is THE owner of "is the ssh session up" — the fact that used to
// have no owner at all. The original Dial captured one *ssh.Client in the
// gRPC dialer closure; when that session died (laptop sleep, network change,
// remote sshd restart), every gRPC reconnect attempt tunneled through the
// same dead session and failed forever — the server's fan-in retried with
// perfect backoff against a transport that could never recover, and the
// mount stayed dark until the whole plugin was restarted.
//
// Every consumer of the tunnel (the gRPC channel AND the SOCKS proxy) dials
// through dial(), which re-establishes the ssh layer on demand: a dead
// session is dropped and rebuilt on the next attempt, single-flight under
// mu so concurrent callers don't stampede the remote sshd.
type redialer struct {
	host    string
	user    string
	auth    []ssh.AuthMethod
	hostKey ssh.HostKeyCallback

	mu     sync.Mutex
	client *ssh.Client // nil = not established or known dead
}

// dial opens addr on the remote host through the current ssh session,
// establishing or re-establishing the session as needed. A channel-open
// failure drops the whole session and retries once through a fresh one:
// distinguishing "session dead" from "remote endpoint refused" isn't worth
// the fragility, and the cost is one extra ssh handshake bounded by the
// caller's (gRPC's) backoff.
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
	// direct-streamlocal@openssh.com: the remote's federation door is a
	// unix socket, never a port (2026-08-26).
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

// drop forgets a dead session (only if it is still the current one — a
// concurrent caller may already have re-established) and closes it so any
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
		// FIRST (recomputed each dial — the user may have just fixed the
		// file), or a server with several host keys can present one the
		// file doesn't hold and strict verification reports "key
		// mismatch" on a perfectly good host (hostalgos.go).
		HostKeyAlgorithms: hostKeyAlgorithmsFor(r.hostKey, r.host),
		// Without a timeout a black-holing network (dropped NAT mapping,
		// sleeping laptop's dead route) would hang the handshake — and mu —
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
// export and a closer. (The loopback SOCKS proxy that used to ride along is
// gone — 2026-07-26, owner decision 2: live url tiles always browse from the
// host's own network.) The client speaks the remote's qualified ids
// verbatim; the host server's transit qualification prepends this plugin's
// uuid on the way back to the local client, so chains compose one segment
// per hop.
//
// Config-shaped failures (unreadable key, bad known_hosts) fail here, at
// spawn, where they are a misconfiguration to surface. The ssh session
// itself is established LAZILY on first use and re-established after any
// death (see redialer) — so a mount whose remote is down at spawn, or whose
// tunnel dies later, heals by itself the moment the remote is reachable
// again; until then every RPC fails loudly and the server's fan-in health
// machinery (issue #47) tells the user.
func Dial(cfg Config) (client gridwellv1.GridwellClient, closer func(), err error) {
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

	// A fixed passthrough target: grpc's resolvers cannot carry a socket
	// path (they would strip its leading slash), and the dialer never
	// needs it — it opens cfg.Addr on the remote host through the
	// (self-healing) SSH session, whatever grpc hands it.
	conn, err := grpc.NewClient("passthrough:///federation",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return rd.dial("unix", cfg.Addr)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// HTTP/2 pings through the tunnel: a session that dies WITHOUT an
		// error (sleep, NAT expiry) would otherwise leave long-lived streams
		// — the event Subscribe above all — blocked in Recv forever, which
		// presents as tiles silently going stale with no retry and no health
		// notice. The ping timeout turns that into Unavailable, the fan-in
		// retries, and the retry's dial rebuilds the ssh session.
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
		// A mount is one user's tunnel, not a fleet: cap the reconnect
		// backoff well below gRPC's 2-minute default so a healed network
		// means a healed mount in seconds.
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
	return gridwellv1.NewGridwellClient(conn), closer, nil
}

// dialDirect is the DIRECT transport: a plain gRPC connection to another
// node's federation socket on this machine, same keepalive and healing
// posture as the tunnel — a dead peer surfaces as Unavailable (never a
// silent stale stream) and a revived one heals in seconds. No auth on
// this path: the socket's 0600 mode is the gate (same uid only); across
// machines the ssh bridge is the one authenticated transport.
func dialDirect(addr string) (gridwellv1.GridwellClient, func(), error) {
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
	return gridwellv1.NewGridwellClient(conn), func() { _ = conn.Close() }, nil
}
