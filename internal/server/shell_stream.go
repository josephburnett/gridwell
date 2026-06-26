package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// The shell PTY now lives in the plugin that owns the tile (OpenShell). The
// server is a pure bridge: the browser can't speak bidirectional gRPC over
// fetch, so the client↔server hop stays a WebSocket, and this handler shuttles
// its bytes to and from the owning plugin's OpenShell stream. No tmux, no PTY,
// no session lifecycle here — that all moved behind the interface.

const (
	minShellCols      = 20
	minShellRows      = 5
	defaultShellCols  = 80
	defaultShellRows  = 24
	shellWriteTimeout = 30 * time.Second
)

// shellStream is the /rpc/ShellStream WebSocket handler. Protocol:
//
//   - Binary frames either direction: stdin (client→server→plugin) and PTY
//     output (plugin→server→client) — raw byte passthrough.
//   - Text frames, client→server: ShellStreamMessage (JSON); only "resize" is
//     recognized, forwarded as an OpenShell resize.
func (s *Server) shellStream(w http.ResponseWriter, r *http.Request) {
	// The wire tile_id is the globally-qualified "<plugin-uuid>/<id>"; route it
	// to the owning plugin, which holds the PTY.
	tileID := r.URL.Query().Get("tile_id")
	client, local, ok := s.clientForID(tileID)
	if !ok {
		http.Error(w, "missing or invalid tile_id", http.StatusBadRequest)
		return
	}
	tr, err := client.GetTile(r.Context(), &pb.GetTileRequest{TileId: local})
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	if tr.Tile.Kind != rpc.KindShell {
		http.Error(w, "not a shell tile", http.StatusBadRequest)
		return
	}
	cols, rows := parseShellSize(r.URL.Query())

	// Enforce same-origin: the renderer loads from http://127.0.0.1:<port> (the
	// same origin as this server), so its WebSocket is same-origin. A non-browser
	// client (tests, a CLI) sends no Origin header and is also accepted. What
	// this rejects is any other web page cross-origin-connecting to grab a PTY.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"127.0.0.1:*", "localhost:*"},
	})
	if err != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := client.OpenShell(ctx)
	if err != nil {
		log.Printf("[shellstream] open tile=%s err=%v", tileID, err)
		_ = conn.Close(websocket.StatusInternalError, "open shell failed")
		return
	}
	// First message binds the tile + initial size (data empty).
	if err := stream.Send(&pb.OpenShellRequest{TileId: local, Resize: &pb.PTYSize{Cols: int32(cols), Rows: int32(rows)}}); err != nil {
		log.Printf("[shellstream] bind tile=%s err=%v", tileID, err)
		_ = conn.Close(websocket.StatusInternalError, "bind shell failed")
		return
	}
	log.Printf("[shellstream] attach tile=%s", tileID)

	writerDone := make(chan struct{})
	go shellStreamWriteLoop(ctx, cancel, conn, stream, tileID, writerDone)
	shellStreamReadLoop(ctx, conn, stream, tileID)

	cancel()
	_ = stream.CloseSend()
	<-writerDone
	closeCode := websocket.StatusNormalClosure
	if errors.Is(ctx.Err(), context.Canceled) {
		// A plugin-side gone session surfaces as a stream error → policy close so
		// the wasm flips the refresh button off.
		closeCode = websocket.StatusNormalClosure
	}
	_ = conn.Close(closeCode, "")
}

// shellStreamWriteLoop ferries plugin PTY output (OpenShell responses) to the WS
// as binary frames. Exits on stream EOF/error or context cancel, cancelling the
// reader so the handler tears down.
func shellStreamWriteLoop(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, stream pb.Gridwell_OpenShellClient, tileID string, done chan<- struct{}) {
	defer close(done)
	defer cancel()
	for {
		resp, err := stream.Recv()
		if err != nil {
			log.Printf("[shellstream] writer-exit tile=%s err=%v", tileID, err)
			return
		}
		if len(resp.Data) == 0 {
			continue
		}
		wctx, wcancel := context.WithTimeout(ctx, shellWriteTimeout)
		werr := conn.Write(wctx, websocket.MessageBinary, resp.Data)
		wcancel()
		if werr != nil {
			log.Printf("[shellstream] write tile=%s err=%v", tileID, werr)
			return
		}
	}
}

// shellStreamReadLoop reads from the WS: binary frames forward as stdin; text
// frames are ShellStreamMessage control signals (resize). Both become OpenShell
// requests to the plugin.
func shellStreamReadLoop(ctx context.Context, conn *websocket.Conn, stream pb.Gridwell_OpenShellClient, tileID string) {
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			log.Printf("[shellstream] reader-exit tile=%s err=%v", tileID, err)
			return
		}
		switch mt {
		case websocket.MessageBinary:
			if err := stream.Send(&pb.OpenShellRequest{Data: data}); err != nil {
				log.Printf("[shellstream] forward tile=%s err=%v", tileID, err)
				return
			}
		case websocket.MessageText:
			var msg rpc.ShellStreamMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				log.Printf("[shellstream] bad-json tile=%s err=%v", tileID, err)
				continue
			}
			if msg.Kind == "resize" {
				cols, rows := clampShell(msg.Cols, msg.Rows)
				if err := stream.Send(&pb.OpenShellRequest{Resize: &pb.PTYSize{Cols: int32(cols), Rows: int32(rows)}}); err != nil {
					log.Printf("[shellstream] resize tile=%s err=%v", tileID, err)
					return
				}
			}
		}
	}
}

// parseShellSize reads cols/rows from the query string, clamping to a minimum so
// a misbehaving client can't drive the PTY to a broken winsize. Empty /
// unparseable / too-small values become the shell defaults.
func parseShellSize(q map[string][]string) (uint16, uint16) {
	parse := func(name string, dflt, min uint16) uint16 {
		vs, ok := q[name]
		if !ok || len(vs) == 0 {
			return dflt
		}
		n, err := strconv.Atoi(vs[0])
		if err != nil || n <= 0 {
			return dflt
		}
		if n < int(min) {
			n = int(min)
		}
		if n > 65535 {
			n = 65535
		}
		return uint16(n)
	}
	return parse("cols", defaultShellCols, minShellCols), parse("rows", defaultShellRows, minShellRows)
}

func clampShell(cols, rows uint16) (uint16, uint16) {
	if cols < minShellCols {
		cols = minShellCols
	}
	if rows < minShellRows {
		rows = minShellRows
	}
	return cols, rows
}
