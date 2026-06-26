package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/localdb"
	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/shellsvc"
	"github.com/josephburnett/gridwell/internal/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/store"
	"github.com/josephburnett/gridwell/internal/tmux"
)

// The PTY now lives in the localdb plugin (OpenShell); the server only bridges
// the browser WebSocket to it. These tests drive the full path — WS → server →
// gRPC OpenShell → localdb → fake streamer — so a byte typed in comes back out.

const shellPluginUUID = "shelltest-uuid"

// newShellBridgeServer builds an httptest server whose single localdb plugin
// owns a fake shell streamer. Returns the server, the fake (to assert on the
// PTY decisions), and the qualified root grid id.
func newShellBridgeServer(t *testing.T) (*httptest.Server, *shellsvctest.FakeStreamer, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	fake := shellsvctest.New()
	p := localdb.New(st, shellsvc.NewManager(fake))
	client, closer, err := plugin.ServeInProcess(p)
	if err != nil {
		t.Fatalf("ServeInProcess: %v", err)
	}
	t.Cleanup(closer)

	reg := plugin.NewRegistry()
	reg.Register(shellPluginUUID, "localdb", client, nil)
	srv := New(reg, Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	info, err := client.Info(context.Background(), &pb.InfoRequest{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	return hs, fake, shellPluginUUID + "/" + info.RootGridId
}

func createShellTile(t *testing.T, hs *httptest.Server, root string) (qualifiedID, localID string) {
	t.Helper()
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	tile, err := cl.CreateShell(context.Background(), &rpc.CreateShellRequest{GridID: root, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatalf("CreateShell: %v", err)
	}
	return tile.ID, strings.TrimPrefix(tile.ID, shellPluginUUID+"/")
}

func shellStreamURL(hs *httptest.Server, tileID string, cols, rows int) string {
	return "ws://" + strings.TrimPrefix(hs.URL, "http://") +
		"/rpc/ShellStream?tile_id=" + tileID +
		"&cols=" + strconv.Itoa(cols) + "&rows=" + strconv.Itoa(rows)
}

func waitForSession(t *testing.T, fake *shellsvctest.FakeStreamer) *shellsvctest.FakeSession {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s := fake.LastSession(); s != nil {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no shell session opened within 2s")
	return nil
}

// TestShellStreamRejectsCrossOrigin: a WebSocket carrying a foreign Origin (a
// web page on the machine trying to reach the PTY) must be refused.
func TestShellStreamRejectsCrossOrigin(t *testing.T) {
	hs, _, root := newShellBridgeServer(t)
	tileID, _ := createShellTile(t, hs, root)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	hdr := http.Header{"Origin": []string{"http://evil.example.com"}}
	conn, _, err := websocket.Dial(ctx, shellStreamURL(hs, tileID, 80, 24), &websocket.DialOptions{HTTPHeader: hdr})
	if err == nil {
		conn.Close(websocket.StatusNormalClosure, "")
		t.Fatal("cross-origin WebSocket was accepted; want rejected")
	}
}

func TestShellStreamAllowsLoopbackOrigin(t *testing.T) {
	hs, _, root := newShellBridgeServer(t)
	tileID, _ := createShellTile(t, hs, root)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	hdr := http.Header{"Origin": []string{"http://127.0.0.1:9999"}}
	conn, _, err := websocket.Dial(ctx, shellStreamURL(hs, tileID, 80, 24), &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("loopback origin rejected: %v", err)
	}
	conn.Close(websocket.StatusNormalClosure, "")
}

// TestShellStreamRefusesNonShellTile: the server checks the tile kind before
// opening a PTY stream.
func TestShellStreamRefusesNonShellTile(t *testing.T) {
	hs, _, root := newShellBridgeServer(t)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	textTile, err := cl.CreateText(context.Background(), &rpc.CreateTextRequest{GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("hi")})
	if err != nil {
		t.Fatalf("CreateText: %v", err)
	}
	_, resp, err := websocket.Dial(context.Background(), shellStreamURL(hs, textTile.ID, 80, 24), nil)
	if err == nil {
		t.Fatal("non-shell tile accepted; want rejected")
	}
	if resp != nil && resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestShellStreamRoundTripsBytes is the end-to-end proof that PTY bytes cross
// the interface: WS in → server → OpenShell → plugin → fake echo → back out.
func TestShellStreamRoundTripsBytes(t *testing.T) {
	hs, fake, root := newShellBridgeServer(t)
	tileID, _ := createShellTile(t, hs, root)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, shellStreamURL(hs, tileID, 100, 40), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	sess := waitForSession(t, fake)
	if sess.OpenMode != tmux.ModeCreate {
		t.Errorf("fresh tile opened in %v, want ModeCreate", sess.OpenMode)
	}

	if err := conn.Write(ctx, websocket.MessageBinary, []byte("echo me")); err != nil {
		t.Fatalf("ws write: %v", err)
	}
	mt, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	if mt != websocket.MessageBinary || string(data) != "echo me" {
		t.Errorf("round trip = (%v, %q), want (binary, %q)", mt, data, "echo me")
	}
}

// TestShellStreamForwardsResize: a JSON resize control frame reaches the PTY.
func TestShellStreamForwardsResize(t *testing.T) {
	hs, fake, root := newShellBridgeServer(t)
	tileID, _ := createShellTile(t, hs, root)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, shellStreamURL(hs, tileID, 80, 24), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	sess := waitForSession(t, fake)

	msg, _ := json.Marshal(rpc.ShellStreamMessage{Kind: "resize", Cols: 120, Rows: 50})
	if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
		t.Fatalf("ws write resize: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rs := sess.Resizes(); len(rs) > 0 {
			if rs[len(rs)-1] != [2]uint16{120, 50} {
				t.Errorf("resize = %v, want [120 50]", rs[len(rs)-1])
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("resize not forwarded within 2s")
}

// TestShellStreamSnapshottedTileAttaches: a tile that already has a frozen
// snapshot and a live session attaches (ModeAttach) rather than spawning fresh.
func TestShellStreamSnapshottedTileAttaches(t *testing.T) {
	hs, fake, root := newShellBridgeServer(t)
	tileID, localID := createShellTile(t, hs, root)

	// Freeze a snapshot (PreviewBlobId != 0) and mark the session alive.
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	if _, err := cl.SetShellPreview(context.Background(), &rpc.SetShellPreviewRequest{TileID: tileID, Version: 0, JPEG: []byte("jpegbytes")}); err != nil {
		t.Fatalf("SetShellPreview: %v", err)
	}
	fake.SetAlive(localID, true)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, shellStreamURL(hs, tileID, 80, 24), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	sess := waitForSession(t, fake)
	if sess.OpenMode != tmux.ModeAttach {
		t.Errorf("snapshotted+live tile opened in %v, want ModeAttach", sess.OpenMode)
	}
}
