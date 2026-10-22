package local_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// The detach seam: OpenShell's Release fires the title capture before
// OpenShell returns, so a failure there is the attachment's end, which the
// shell API sends as the exit. These run the whole path: stream, manager,
// capture, store.

// captureFailed is how the attachment's end names a capture that failed.
const captureFailed = "the shell's name was not updated"

func TestShellTitleCaptureStampsOnDetach(t *testing.T) {
	fake := shellsvctest.New()
	fake.PaneCmd = "vim"
	p, tileID := shellPluginWithTile(t, fake)

	if err := openShellAndDetach(t, p, tileID); err != nil {
		t.Errorf("OpenShell = %v, want a capture that landed to end clean", err)
	}
	if got := altOf(t, p, tileID); got != "vim" {
		t.Errorf("alt = %q, want the captured foreground command %q", got, "vim")
	}
}

func TestShellTitleCaptureEndsWithAFailedRead(t *testing.T) {
	fake := shellsvctest.New()
	fake.PaneCmd = "vim"
	fake.PaneErr = errors.New("tmux: no server running")
	p, tileID := shellPluginWithTile(t, fake)

	err := openShellAndDetach(t, p, tileID)

	if err == nil || !strings.Contains(err.Error(), captureFailed) || !strings.Contains(err.Error(), "no server running") {
		t.Errorf("OpenShell = %v, want the capture and the reason", err)
	}
	if got := altOf(t, p, tileID); got != "shell" {
		t.Errorf("alt = %q, want the name the tile already had", got)
	}
}

// The other half of the capture: the store write. The user deletes the tile
// and empties the trash while the shell is still attached, so the row the
// capture would stamp is gone by the time it runs.
func TestShellTitleCaptureEndsWithAFailedWrite(t *testing.T) {
	fake := shellsvctest.New()
	fake.PaneCmd = "vim"
	p, tileID := shellPluginWithTile(t, fake)

	err := openShellAndDetach(t, p, tileID, func() {
		for range 2 { // to the trash, then out of it
			if _, err := p.DeleteTile(context.Background(), &gridwellv1.DeleteTileRequest{TileId: tileID}); err != nil {
				t.Errorf("DeleteTile: %v", err)
			}
		}
	})

	if err == nil || !strings.Contains(err.Error(), captureFailed) || !strings.Contains(err.Error(), "not found") {
		t.Errorf("OpenShell = %v, want the capture and the store's reason", err)
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

// openShellAndDetach opens the tile's shell, runs beforeDetach with the tmux
// live, then ends the stream and returns how OpenShell ended. OpenShell
// releases before it returns, so the capture has run by then.
func openShellAndDetach(t *testing.T, p *local.Plugin, tileID string, beforeDetach ...func()) error {
	t.Helper()
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

	<-down // the fake echoed: the tmux is attached
	for _, fn := range beforeDetach {
		fn()
	}
	cancel()
	return <-done
}

func altOf(t *testing.T, p *local.Plugin, tileID string) string {
	t.Helper()
	got, err := p.GetTile(context.Background(), &gridwellv1.GetTileRequest{TileId: tileID})
	if err != nil {
		t.Fatalf("GetTile: %v", err)
	}
	return got.Tile.AltText
}
