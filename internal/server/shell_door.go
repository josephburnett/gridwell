package server

// The /shell door: PTY bytes on the web door, like every other primitive. An
// attach is an ordinary page request on the gated mux, so there is no second
// gate to keep in step, and it routes through the one shell route both doors
// share. The grammar is client/shellwire, read by both ends.

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"
	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/client/shellwire"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/trace"
)

// defaultShellWriteTimeout bounds one PTY-output frame write, so a viewer
// whose socket has stopped draining cannot pin the plugin's PTY reader
// forever. New copies it onto the Server, which is what the door reads, so a
// seam test can wedge a socket on a server of its own rather than waiting the
// bound out or lowering it under a door that is already serving.
const defaultShellWriteTimeout = 30 * time.Second

// shellRoute answers whether this attach may happen and who owns the PTY. It is
// side-effect-free on purpose: the WebSocket door answers it before upgrading,
// and a refused upgrade must not have spawned a PTY.
func (s *Server) shellRoute(tileID string) (ns namespace.Namespace, local string, err error) {
	if s.cfg.DisableShells {
		return nil, "", status.Error(gcodes.PermissionDenied, "shell tiles are disabled on this node (server.yaml disable_shells)")
	}
	c, local, ok := s.clientForID(tileID)
	if !ok {
		return nil, "", status.Errorf(gcodes.NotFound, "no plugin for shell tile %q", tileID)
	}
	return c, local, nil
}

// openShellBound sends the bind, then the caller's own frames. This is where a
// PTY is acquired, so it runs after every refusal.
func (s *Server) openShellBound(ctx context.Context, ns namespace.Namespace, local string, first *pb.OpenShellRequest,
	recv func() (*pb.OpenShellRequest, error), send func(*pb.OpenShellResponse) error) error {
	bind := &pb.OpenShellRequest{TileId: local, Data: first.Data, Resize: first.Resize}
	sentBind := false
	return ns.OpenShell(ctx, func() (*pb.OpenShellRequest, error) {
		if !sentBind {
			sentBind = true
			return bind, nil
		}
		return recv()
	}, send)
}

// openShellRoute is the shell route whole: refusal, resolution, bind.
func (s *Server) openShellRoute(ctx context.Context, first *pb.OpenShellRequest,
	recv func() (*pb.OpenShellRequest, error), send func(*pb.OpenShellResponse) error) error {
	ns, local, err := s.shellRoute(first.TileId)
	if err != nil {
		return err
	}
	return s.openShellBound(ctx, ns, local, first, recv, send)
}

// shellDoor serves shellwire.Path. Same-origin is websocket.Accept's default;
// a client that sends no Origin is not a browser and is accepted.
func (s *Server) shellDoor() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attach, err := shellwire.ParseAttach(r.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// The stream outlives the http.Request: net/http may cancel r.Context()
		// the moment the connection is hijacked, which would tear the PTY down
		// at the instant it attached.
		ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
		defer cancel()

		bind := &pb.OpenShellRequest{TileId: attach.TileID}
		if attach.Cols > 0 || attach.Rows > 0 {
			bind.Resize = &pb.PTYSize{Cols: int32(attach.Cols), Rows: int32(attach.Rows)}
		}
		// Resolve before the upgrade so a refusal lands as an HTTP status on
		// the handshake, which the client sees as a failed dial rather than a
		// socket that opens and dies.
		ns, local, err := s.shellRoute(attach.TileID)
		kv := map[string]string{"tile": attach.TileID}
		if err != nil {
			trace.Emit("shelldoor", "pty", "refused: "+err.Error(), kv)
			httpStatusError(w, err)
			return
		}

		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			trace.Emit("shelldoor", "pty", "upgrade failed: "+err.Error(), kv)
			// Accept has already written the failure to w. Nothing has touched
			// a PTY yet, and nothing will.
			cancel()
			return
		}
		conn.SetReadLimit(shellwire.ReadLimit)

		// The PTY is acquired only now: an upgrade nobody completed must never
		// leave a session behind.
		trace.Emit("shelldoor", "pty", "open", kv)
		msg, gone := pumpShell(ctx, cancel, conn, s.shellWriteTimeout, func(recv func() (*pb.OpenShellRequest, error), send func(*pb.OpenShellResponse) error) error {
			return s.openShellBound(ctx, ns, local, bind, recv, send)
		})
		trace.Emit("shelldoor", "pty", closeVerdict(msg, gone), kv)
		writeShellExit(conn, s.shellWriteTimeout, msg, gone)
	})
}

// closeVerdict is the one spelling of how an attachment ended, for the record.
func closeVerdict(message string, sessionGone bool) string {
	switch {
	case sessionGone:
		return "close, session gone: " + message
	case message != "":
		return "close: " + message
	}
	return "close"
}

// writeShellExit carries the verdict a WebSocket close code cannot: why the
// attachment ended, and whether the session itself is gone.
func writeShellExit(conn *websocket.Conn, bound time.Duration, message string, sessionGone bool) {
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	_ = conn.Write(ctx, websocket.MessageText, shellwire.EncodeExit(message, sessionGone))
	cancel()
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// pumpShell runs one attachment and reports the end verdict. It deliberately
// does not cancel ctx on the way out: the verdict still has to reach the client
// as an exit frame, and a websocket read whose context is cancelled tears the
// connection down where it stands, so "the session is gone" would arrive as
// "something broke". The caller cancels once the frame is on the wire.
func pumpShell(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, bound time.Duration,
	attach func(recv func() (*pb.OpenShellRequest, error), send func(*pb.OpenShellResponse) error) error) (message string, sessionGone bool) {
	// A write failure is the viewer's socket breaking, not the PTY ending, and
	// is remembered separately so it is reported as itself.
	var writeErr error
	err := attach(
		func() (*pb.OpenShellRequest, error) { return shellReadFrame(ctx, cancel, conn) },
		func(resp *pb.OpenShellResponse) error {
			if len(resp.Data) == 0 {
				return nil
			}
			wctx, wcancel := context.WithTimeout(ctx, bound)
			werr := conn.Write(wctx, websocket.MessageBinary, resp.Data)
			wcancel()
			if werr != nil {
				writeErr = werr
			}
			return werr
		})
	if writeErr != nil {
		return "shell output: " + writeErr.Error(), false
	}
	return shellEndVerdict(err)
}

// shellReadFrame reads binary as stdin and text as a shellwire control. An
// unreadable frame ends the attachment rather than being dropped, because a
// client speaking an unknown grammar is a bug to surface.
func shellReadFrame(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn) (*pb.OpenShellRequest, error) {
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			// An ordinary detach says nothing; any other end is worth a line,
			// or the server has no record of why the attachment vanished.
			if ctx.Err() == nil && websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				log.Printf("gridwell: shell door: the client's stream ended: %v", err)
			}
			cancel()
			return nil, io.EOF
		}
		switch mt {
		case websocket.MessageBinary:
			return &pb.OpenShellRequest{Data: data}, nil
		case websocket.MessageText:
			c, derr := shellwire.DecodeControl(data)
			if derr != nil {
				log.Printf("[shell door] bad control frame: %v", derr)
				_ = conn.Close(websocket.StatusUnsupportedData, "bad control frame")
				cancel()
				return nil, io.EOF
			}
			if c.Kind != shellwire.KindResize {
				continue // KindExit is the server's word; a client saying it means nothing
			}
			// Not clamped here: shellsvc.ClampSize, in the namespace that owns
			// the PTY, is the one owner of the bounds.
			return &pb.OpenShellRequest{Resize: &pb.PTYSize{Cols: int32(c.Cols), Rows: int32(c.Rows)}}, nil
		}
	}
}

// shellEndVerdict is the one classification of why the attachment ended:
// NotFound and FailedPrecondition are the PTY owner's codes for "this session
// no longer exists", anything else a failure the client may retry.
func shellEndVerdict(err error) (message string, sessionGone bool) {
	if err == nil || errors.Is(err, io.EOF) {
		return "", false
	}
	st, ok := status.FromError(err)
	if !ok {
		return err.Error(), false
	}
	switch st.Code() {
	case gcodes.OK:
		return "", false
	case gcodes.NotFound, gcodes.FailedPrecondition:
		return st.Message(), true
	default:
		return st.Message(), false
	}
}
