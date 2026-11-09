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

// A name that is no regular file, directory or symlink, a FIFO or a socket
// here, lists as a read-only text tile with no body and no picture, and
// nothing opens it, whatever its name says it is. A FIFO's nonblocking open
// succeeds and a socket's fails, so each is its own case.
func TestASpecialFileIsANameAndIsNeverOpened(t *testing.T) {
	makers := map[string]func(t *testing.T, path string){
		"pipe": func(t *testing.T, path string) {
			if err := syscall.Mkfifo(path, 0o644); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
		},
		"sock": func(t *testing.T, path string) {
			fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
			if err == nil {
				t.Cleanup(func() { syscall.Close(fd) })
				err = syscall.Bind(fd, &syscall.SockaddrUnix{Name: path})
			}
			if err != nil {
				t.Skipf("bind unix socket: %v", err)
			}
		},
	}
	for stem, make := range makers {
		t.Run(stem, func(t *testing.T) { specialIsNeverOpened(t, stem, make) })
	}
}

func specialIsNeverOpened(t *testing.T, stem string, make func(*testing.T, string)) {
	f := newFixture(t)
	txt, png, mp4 := stem+".txt", stem+".png", stem+".mp4"
	for _, name := range []string{txt, png, mp4} {
		make(t, f.path(name))
	}
	ctx := context.Background()
	g := f.grid(f.root)
	for _, name := range []string{txt, png, mp4} {
		tl := tileIn(t, g, f.path(name))
		if tl.Kind != rpc.KindText || tl.ServesPage || tl.PreviewBlobId != 0 || !strings.Contains(tl.ReadOnly, "not a regular file") {
			t.Errorf("%s lists as %+v, want a read-only text tile with no page or picture", name, tl)
		}
	}
	var body []byte
	err := within(t, "ReadContent", func() error {
		return f.s.ReadContent(ctx, &pb.ReadContentRequest{TileId: idOf(f.path(txt))}, func(c *pb.ContentChunk) error {
			body = append(body, c.Data...)
			return nil
		})
	})
	if err != nil || len(body) != 0 {
		t.Errorf("ReadContent(%s) = %q, %v; want no body", txt, body, err)
	}
	var jpeg []byte
	if err := within(t, "GetTilePreview", func() error {
		resp, err := f.s.GetTilePreview(ctx, &pb.GetTilePreviewRequest{TileId: idOf(f.path(png))})
		jpeg = resp.GetJpeg()
		return err
	}); err != nil || len(jpeg) != 0 {
		t.Errorf("GetTilePreview(%s) = %d bytes, %v; want no picture", png, len(jpeg), err)
	}
	if err := within(t, "ServeContent", func() error {
		return f.s.ServeContent(ctx, &pb.ServeContentRequest{TileId: idOf(f.path(mp4))}, func(*pb.ServeContentChunk) error { return nil })
	}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("ServeContent(%s) = %v, want refused as not a regular file", mp4, err)
	}
	if err := within(t, "WriteContent", func() error {
		_, err := writeAll(f.s, idOf(f.path(txt)), "", []byte("x"))
		return err
	}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("WriteContent(%s) = %v, want refused as not a regular file", txt, err)
	}
}
