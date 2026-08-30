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
func (s *Server) shellRoute(tileID string) (client pb.GridwellClient, local string, err error) {
	if s.cfg.DisableShells {
		return nil, "", status.Error(gcodes.PermissionDenied, "shell tiles are disabled on this node (server.yaml disable_shells)")
	}
	c, local, ok := s.clientForID(tileID)
	if !ok {
		return nil, "", status.Errorf(gcodes.NotFound, "no plugin for shell tile %q", tileID)
	}
	return c, local, nil
}

// openShellBound opens the namespace's OpenShell stream and sends the bind
// — the message that names the tile and the initial size. This is where a
// PTY is acquired, so it is the LAST step, after every refusal.
func (s *Server) openShellBound(ctx context.Context, c pb.GridwellClient, local string, first *pb.OpenShellRequest) (pb.Gridwell_OpenShellClient, error) {
	up, err := c.OpenShell(ctx)
	if err != nil {
		return nil, err
	}
	if err := up.Send(&pb.OpenShellRequest{TileId: local, Data: first.Data, Resize: first.Resize}); err != nil {
		return nil, err
	}
	return up, nil
}

// openShellRoute is THE shell route, whole: refusal, resolution, bind. Both
// doors — this browser WebSocket and the node export's bidi gRPC
// (nodeexport.go) — enter through these functions, so a shell on a mounted
// remote behaves identically whichever door asked, and neither door can
// grow its own routing.
func (s *Server) openShellRoute(ctx context.Context, first *pb.OpenShellRequest) (pb.Gridwell_OpenShellClient, error) {
	c, local, err := s.shellRoute(first.TileId)
	if err != nil {
		return nil, err
	}
	return s.openShellBound(ctx, c, local, first)
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
		c, local, err := s.shellRoute(attach.TileID)
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
		up, err := s.openShellBound(ctx, c, local, bind)
		if err != nil {
			msg, gone := shellEndVerdict(err)
			writeShellExit(conn, msg, gone)
			return
		}
		msg, gone := pumpShell(ctx, cancel, conn, up)
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

// pumpShell ferries one attachment both ways until either side ends, and
// reports the END VERDICT: the message to show the user (empty for a clean
// end) and whether the PTY session is GONE for good.
func pumpShell(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, up pb.Gridwell_OpenShellClient) (message string, sessionGone bool) {
	go shellReadLoop(ctx, cancel, conn, up)
	for {
		resp, err := up.Recv()
		if err != nil {
			_ = up.CloseSend()
			cancel()
			return shellEndVerdict(err)
		}
		if len(resp.Data) == 0 {
			continue
		}
		wctx, wcancel := context.WithTimeout(ctx, shellWriteTimeout)
		werr := conn.Write(wctx, websocket.MessageBinary, resp.Data)
		wcancel()
		if werr != nil {
			_ = up.CloseSend()
			cancel()
			// The viewer's socket broke; nothing to tell it.
			return "", false
		}
	}
}

// shellReadLoop forwards the client's frames up: binary is stdin, text is a
// shellwire control. A frame this end cannot read ENDS the attachment
// rather than being dropped — a client speaking a grammar the door does not
// know is a bug to surface, not to ignore (charter §6).
func shellReadLoop(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, up pb.Gridwell_OpenShellClient) {
	defer cancel()
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			_ = up.CloseSend()
			return
		}
		switch mt {
		case websocket.MessageBinary:
			if err := up.Send(&pb.OpenShellRequest{Data: data}); err != nil {
				return
			}
		case websocket.MessageText:
			c, derr := shellwire.DecodeControl(data)
			if derr != nil {
				log.Printf("[shell door] bad control frame: %v", derr)
				_ = conn.Close(websocket.StatusUnsupportedData, "bad control frame")
				return
			}
			if c.Kind != shellwire.KindResize {
				continue // KindExit is the server's word; a client saying it means nothing.
			}
			// The sizes are NOT clamped here: shellsvc.ClampSize, in the
			// namespace that owns the PTY, is the one owner of the bounds.
			if err := up.Send(&pb.OpenShellRequest{Resize: &pb.PTYSize{Cols: int32(c.Cols), Rows: int32(c.Rows)}}); err != nil {
				return
			}
		}
	}
}

// shellEndVerdict classifies why the upstream stream ended. NotFound /
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
