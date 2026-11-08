//go:build unix

package files

import (
	"context"
	"strings"
	"syscall"
	"testing"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// within fails the test when do has not answered in a second: opening a FIFO
// blocks until a writer comes, which never does.
func within(t *testing.T, what string, do func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- do() }()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatalf("%s did not answer: it opened the FIFO", what)
		return nil
	}
}

// A name that is no regular file, directory or symlink, a FIFO here, lists
// as a read-only text tile with no body and no picture, and nothing opens
// it, whatever its name says it is.
func TestASpecialFileIsANameAndIsNeverOpened(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"pipe.txt", "pipe.png", "pipe.mp4"} {
		if err := syscall.Mkfifo(f.path(name), 0o644); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
	}
	ctx := context.Background()
	g := f.grid(f.root)
	for _, name := range []string{"pipe.txt", "pipe.png", "pipe.mp4"} {
		tl := tileIn(t, g, f.path(name))
		if tl.Kind != rpc.KindText || tl.ServesPage || tl.PreviewBlobId != 0 || !strings.Contains(tl.ReadOnly, "not a regular file") {
			t.Errorf("%s lists as %+v, want a read-only text tile with no page or picture", name, tl)
		}
	}
	var body []byte
	err := within(t, "ReadContent", func() error {
		return f.s.ReadContent(ctx, &pb.ReadContentRequest{TileId: idOf(f.path("pipe.txt"))}, func(c *pb.ContentChunk) error {
			body = append(body, c.Data...)
			return nil
		})
	})
	if err != nil || len(body) != 0 {
		t.Errorf("ReadContent(pipe.txt) = %q, %v; want no body", body, err)
	}
	_ = within(t, "GetTilePreview", func() error {
		_, err := f.s.GetTilePreview(ctx, &pb.GetTilePreviewRequest{TileId: idOf(f.path("pipe.png"))})
		return err
	})
	if err := within(t, "ServeContent", func() error {
		return f.s.ServeContent(ctx, &pb.ServeContentRequest{TileId: idOf(f.path("pipe.mp4"))}, func(*pb.ServeContentChunk) error { return nil })
	}); err == nil {
		t.Error("ServeContent(pipe.mp4) served a page")
	}
	if err := within(t, "WriteContent", func() error {
		_, err := writeAll(f.s, idOf(f.path("pipe.txt")), "", []byte("x"))
		return err
	}); err == nil {
		t.Error("WriteContent(pipe.txt) replaced the FIFO")
	}
}
