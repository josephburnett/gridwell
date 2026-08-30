package server

// The /shell door: PTY bytes on the WEB door, like every other primitive.
//
// Owner decision (docs/simplify-plan.md, 2026-08-29 — "Shells are a
// WebSocket on the web door"): a shell attach is an ordinary page request.
// It rides the page's own origin, behind the SAME auth cookie as every
// other request (auth.go — this handler is mounted on the gated mux, so
// there is no second gate to keep in step), and it routes through the one
// shell route every door shares. This REVERSES 2026-07-26 ("PTY bytes ride
// the Electron main process's gRPC OpenShell stream"), whose consequence
// was that only the desktop had shells: the primitive set is supposed to be
// closed and identical on every host.
//
// The grammar — the address, the frames, the exit verdict — is
// client/shellwire, read by BOTH ends, so it cannot drift; the seam test
// (shell_door_seam_test.go) dials this handler with the real client stack.

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
)

// shellWriteTimeout bounds one PTY-output frame write. A viewer whose
// socket has stopped draining must not pin the plugin's PTY reader forever.
const shellWriteTimeout = 30 * time.Second

// shellRoute answers "may this attach happen, and who owns the PTY" with
// NO side effects: the node-wide refusal (server.yaml disable_shells —
// every PTY door on this node is closed, whichever namespace would hold the
// session) and the namespace that owns the tile, one segment peeled exactly
// like every other route. Kept side-effect-free on purpose: the WebSocket
// door answers this BEFORE upgrading, and a refused upgrade must not have
// spawned a PTY.
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

// openShellBound attaches to the namespace that owns the tile: the BIND —
// the message that names the tile and the initial size — goes first, then
// the caller's own frames. This is where a PTY is acquired, so it is the
// LAST step, after every refusal. It returns when either side ends.
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

// openShellRoute is THE shell route, whole: refusal, resolution, bind. Both
// doors — this browser WebSocket and the federation codec's bidi gRPC
// (router.OpenShell) — enter through these functions, so a shell on a
// mounted remote behaves identically whichever door asked, and neither door
// can grow its own routing.
func (s *Server) openShellRoute(ctx context.Context, first *pb.OpenShellRequest,
	recv func() (*pb.OpenShellRequest, error), send func(*pb.OpenShellResponse) error) error {
	ns, local, err := s.shellRoute(first.TileId)
	if err != nil {
		return err
	}
	return s.openShellBound(ctx, ns, local, first, recv, send)
}

// shellDoor serves shellwire.Path. Same-origin is enforced by
// websocket.Accept's default (Origin host must equal Host); a client that
// sends no Origin at all — a test, a CLI — is not a browser and is
// accepted, exactly as the /content/ door treats non-browser callers.
func (s *Server) shellDoor() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attach, err := shellwire.ParseAttach(r.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// The stream outlives the http.Request: net/http may cancel
		// r.Context() the moment this handler's connection is hijacked, and
		// that would tear the PTY down at the instant it attached.
		ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
		defer cancel()

		bind := &pb.OpenShellRequest{TileId: attach.TileID}
		if attach.Cols > 0 || attach.Rows > 0 {
			bind.Resize = &pb.PTYSize{Cols: int32(attach.Cols), Rows: int32(attach.Rows)}
		}
		// Resolve BEFORE the upgrade so a refusal (shells disabled, no such
		// namespace) lands as an HTTP status on the handshake — an error the
		// client sees as a failed dial, not a socket that opens and dies.
		ns, local, err := s.shellRoute(attach.TileID)
		if err != nil {
			httpStatusError(w, err)
			return
		}

		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			// Accept has already written the failure to w (a foreign Origin
			// lands here). Nothing has touched a PTY yet, and nothing will.
			cancel()
			return
		}
		conn.SetReadLimit(shellwire.ReadLimit)

		// The PTY is acquired only now: an upgrade nobody completed must
		// never leave a session behind.
		msg, gone := pumpShell(ctx, cancel, conn, func(recv func() (*pb.OpenShellRequest, error), send func(*pb.OpenShellResponse) error) error {
			return s.openShellBound(ctx, ns, local, bind, recv, send)
		})
		writeShellExit(conn, msg, gone)
	})
}

// writeShellExit sends the door's LAST word and closes: the exit frame
// carries the verdict a WebSocket close code cannot (why it ended, and
// whether the session itself is gone). Best-effort — a client that closed
// first already knows.
func writeShellExit(conn *websocket.Conn, message string, sessionGone bool) {
	ctx, cancel := context.WithTimeout(context.Background(), shellWriteTimeout)
	_ = conn.Write(ctx, websocket.MessageText, shellwire.EncodeExit(message, sessionGone))
	cancel()
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// pumpShell runs one attachment and reports the END VERDICT: the message
// to show the user (empty for a clean end) and whether the PTY session is
// GONE for good. attach is the bound OpenShell call; the two callbacks are
// the WebSocket read and write ends.
//
// It deliberately does NOT cancel ctx on the way out. The verdict still has
// to reach the client as an exit frame, and a coder/websocket read whose
// context is cancelled tears the connection down where it stands — the
// client would see a bare EOF instead of the verdict, and "the session is
// gone" would arrive as "something broke". The caller cancels once the
// frame is on the wire.
func pumpShell(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn,
	attach func(recv func() (*pb.OpenShellRequest, error), send func(*pb.OpenShellResponse) error) error) (message string, sessionGone bool) {
	// A WRITE failure is the viewer's socket breaking, not the PTY ending.
	// Remembered separately so it is reported as itself: a broken pipe that
	// presents as "the terminal closed normally" is the disappearing-failure
	// shape (charter §6). The frame usually cannot land — the socket is the
	// thing that broke — but a local close on the client suppresses the
	// report anyway, so this can only ever add information.
	var writeErr error
	err := attach(
		func() (*pb.OpenShellRequest, error) { return shellReadFrame(ctx, cancel, conn) },
		func(resp *pb.OpenShellResponse) error {
			if len(resp.Data) == 0 {
				return nil
			}
			wctx, wcancel := context.WithTimeout(ctx, shellWriteTimeout)
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

// shellReadFrame reads the client's next frame as an OpenShell message:
// binary is stdin, text is a shellwire control. A frame this end cannot
// read ENDS the attachment rather than being dropped — a client speaking a
// grammar the door does not know is a bug to surface, not to ignore
// (charter §6). An ordinary detach reads as io.EOF: a clean end.
func shellReadFrame(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn) (*pb.OpenShellRequest, error) {
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			// An ordinary detach is a normal close and says nothing. Any
			// OTHER end of the client's side is worth a line: the client
			// sees the attachment vanish, and without this the server has
			// no record of why.
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
				continue // KindExit is the server's word; a client saying it means nothing.
			}
			// The sizes are NOT clamped here: shellsvc.ClampSize, in the
			// namespace that owns the PTY, is the one owner of the bounds.
			return &pb.OpenShellRequest{Resize: &pb.PTYSize{Cols: int32(c.Cols), Rows: int32(c.Rows)}}, nil
		}
	}
}

// shellEndVerdict classifies why the attachment ended. NotFound /
// FailedPrecondition are the codes the PTY owner uses for "this session no
// longer exists" (local.OpenShell maps shellsvc.ErrSessionGone to
// FailedPrecondition); anything else is a transport or plugin failure the
// client must surface but may still retry. The ONE classification — it used
// to live in the Electron dialer's sessionGoneCodes set.
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
