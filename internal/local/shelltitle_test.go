package local_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// The detach seam: OpenShell's Release fires the title capture after the PTY
// is closed, so a failure there has no stream to travel back on and the log is
// the surface. These run the whole path — stream, manager, capture, store.

// captureLog is the one line the detach capture writes when it fails.
const captureLog = "shell title capture"

func TestShellTitleCaptureStampsOnDetach(t *testing.T) {
	fake := shellsvctest.New()
	fake.PaneCmd = "vim"
	p, tileID := shellPluginWithTile(t, fake)

	logs := openShellAndDetach(t, p, tileID)

	if got := altOf(t, p, tileID); got != "vim" {
		t.Errorf("alt = %q, want the captured foreground command %q", got, "vim")
	}
	if strings.Contains(logs, captureLog) {
		t.Errorf("a capture that landed logged: %q", logs)
	}
}

func TestShellTitleCaptureLogsAFailedRead(t *testing.T) {
	fake := shellsvctest.New()
	fake.PaneCmd = "vim"
	fake.PaneErr = errors.New("tmux: no server running")
	p, tileID := shellPluginWithTile(t, fake)

	logs := openShellAndDetach(t, p, tileID)

	if !strings.Contains(logs, captureLog) || !strings.Contains(logs, tileID) || !strings.Contains(logs, "no server running") {
		t.Errorf("log = %q, want the capture, the tile id %s, and the reason", logs, tileID)
	}
	if got := altOf(t, p, tileID); got != "shell" {
		t.Errorf("alt = %q, want the name the tile already had", got)
	}
}

// The other half of the capture: the store write. The user deletes the tile
// and empties the trash while the shell is still attached, so the row the
// capture would stamp is gone by the time it runs.
func TestShellTitleCaptureLogsAFailedWrite(t *testing.T) {
	fake := shellsvctest.New()
	fake.PaneCmd = "vim"
	p, tileID := shellPluginWithTile(t, fake)

	logs := openShellAndDetach(t, p, tileID, func() {
		for range 2 { // to the trash, then out of it
			if _, err := p.DeleteTile(context.Background(), &gridwellv1.DeleteTileRequest{TileId: tileID}); err != nil {
				t.Errorf("DeleteTile: %v", err)
			}
		}
	})

	if !strings.Contains(logs, captureLog) || !strings.Contains(logs, tileID) || !strings.Contains(logs, "not found") {
		t.Errorf("log = %q, want the capture, the tile id %s, and the store's reason", logs, tileID)
	}
}

// shellPluginWithTile is a home over an in-memory store hosting the fake's
// shells, plus one shell tile on the root grid.
func shellPluginWithTile(t *testing.T, fake *shellsvctest.FakeStreamer) (*local.Plugin, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	p := local.New(st, shellsvc.NewManager(fake))
	root := rootGrid(t, p)
	sh, err := p.CreateTile(context.Background(), &gridwellv1.CreateTileRequest{
		GridId: root,
		Tile:   &gridwellv1.Tile{Kind: "shell", X: 0, Y: 0, W: 1, H: 1},
	})
	if err != nil {
		t.Fatalf("CreateTile(shell): %v", err)
	}
	return p, sh.Tile.Id
}

// openShellAndDetach opens the tile's shell, runs beforeDetach with the session
// live, then ends the stream and returns whatever the detach logged. OpenShell
// releases before it returns, so the capture has run by then.
func openShellAndDetach(t *testing.T, p *local.Plugin, tileID string, beforeDetach ...func()) string {
	t.Helper()
	var logbuf bytes.Buffer
	log.SetOutput(&logbuf)
	t.Cleanup(func() { log.SetOutput(io.Discard) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	up := make(chan *gridwellv1.OpenShellRequest, 2)
	up <- &gridwellv1.OpenShellRequest{TileId: tileID, Resize: &gridwellv1.PTYSize{Cols: 80, Rows: 24}}
	up <- &gridwellv1.OpenShellRequest{Data: []byte("x")}
	down := make(chan []byte, 8)
	done := make(chan error, 1)
	go func() {
		done <- p.OpenShell(ctx,
			func() (*gridwellv1.OpenShellRequest, error) {
				select {
				case msg := <-up:
					return msg, nil
				case <-ctx.Done():
					return nil, io.EOF
				}
			},
			func(r *gridwellv1.OpenShellResponse) error {
				down <- r.Data
				return nil
			})
	}()

	<-down // the fake echoed: the session is attached
	for _, fn := range beforeDetach {
		fn()
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("OpenShell: %v", err)
	}
	log.SetOutput(io.Discard)
	return logbuf.String()
}

func altOf(t *testing.T, p *local.Plugin, tileID string) string {
	t.Helper()
	got, err := p.GetTile(context.Background(), &gridwellv1.GetTileRequest{TileId: tileID})
	if err != nil {
		t.Fatalf("GetTile: %v", err)
	}
	return got.Tile.AltText
}
