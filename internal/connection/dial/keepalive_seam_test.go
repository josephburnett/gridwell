package dial

// The silent-death seam: a far side that stops answering without closing the
// socket, which is what a slept laptop or a wedged remote looks like from here.
// The far side is the node's own connection door — server.ListenConnectionDoor
// under server.ConnectionDoorServer — behind a listener that swallows bytes on
// command, so nothing but gRPC keepalive can tell the client anything is wrong.
// Both dials wear grpcDialOptions, so one transport proves the policy.

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// deadDoor serves normally until freeze, after which every byte in either
// direction vanishes and the socket stays open: no close, no reset, no error
// for the client to read. Reads already parked in the kernel are swallowed too,
// so the client's ping is never answered.
type deadDoor struct {
	net.Listener
	frozen chan struct{}
	closed chan struct{}
}

func (d *deadDoor) Accept() (net.Conn, error) {
	c, err := d.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &deadConn{Conn: c, door: d}, nil
}

func (d *deadDoor) freeze() { close(d.frozen) }

func (d *deadDoor) isFrozen() bool {
	select {
	case <-d.frozen:
		return true
	default:
		return false
	}
}

type deadConn struct {
	net.Conn
	door *deadDoor
}

func (c *deadConn) Read(p []byte) (int, error) {
	for {
		if c.door.isFrozen() {
			// Park rather than return: an error here is news, and a dead
			// peer sends none.
			<-c.door.closed
			return 0, io.EOF
		}
		n, err := c.Conn.Read(p)
		if !c.door.isFrozen() {
			return n, err
		}
	}
}

func (c *deadConn) Write(p []byte) (int, error) {
	if c.door.isFrozen() {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

// silentDoor stands the real connection door on a real unix socket and returns
// it with the switch that kills it silently.
func silentDoor(t *testing.T) (string, *deadDoor) {
	t.Helper()
	reg := plugin.NewRegistry()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	reg.Register("ur1", "home", local.New(st, shellsvc.NewManager(shellsvctest.New())), nil)
	srv := servertest.New(t, reg, server.Config{})

	sock := filepath.Join(t.TempDir(), "federation.sock")
	ln, err := server.ListenConnectionDoor(sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	door := &deadDoor{Listener: ln, frozen: make(chan struct{}), closed: make(chan struct{})}
	httpSrv := server.ConnectionDoorServer(srv.ConnectionHandler())
	go httpSrv.Serve(door)
	t.Cleanup(func() {
		close(door.closed)
		httpSrv.Close()
	})
	return sock, door
}

// A Subscribe outlives every RPC on the connection, so it is the stream that a
// silent far side strands. The keepalive ping is the only thing that notices:
// within Time+Timeout the transport dies and Recv returns Unavailable, which is
// what the fan-in retries on. Without the ping this test hangs until the
// context expires — which is the shape of the bug, a mount gone quiet forever.
func TestASilentFarSideFailsAStreamWithinKeepalive(t *testing.T) {
	// grpc-go's floor is 10s, so a bound test costs that much wall clock.
	was := keepaliveParams
	t.Cleanup(func() { keepaliveParams = was })
	keepaliveParams = keepalive.ClientParameters{Time: 10 * time.Second, Timeout: time.Second}
	bound := keepaliveParams.Time + keepaliveParams.Timeout + 5*time.Second

	sock, door := silentDoor(t)
	client, closer, err := Dial(Config{Addr: sock})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(closer)

	ctx, cancel := context.WithTimeout(context.Background(), 2*bound)
	defer cancel()
	live := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- namespace.Follow(ctx, client, &pb.SubscribeRequest{},
			func(*pb.Event) error { return nil }, func() { close(live) })
	}()
	select {
	case <-live:
	case err := <-errc:
		t.Fatalf("Subscribe failed before the far side went silent: %v", err)
	}

	door.freeze()
	select {
	case err := <-errc:
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("stranded stream = %v (code %v), want Unavailable", err, status.Code(err))
		}
		if !strings.Contains(err.Error(), "keepalive") {
			t.Fatalf("stranded stream = %v, want the keepalive ping's verdict; "+
				"grpc-go's liveness behavior may have changed under keepaliveParams", err)
		}
	case <-time.After(bound):
		t.Fatalf("stream still parked in Recv %v after the far side went silent", bound)
	}
}
