package files

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// fixture is a Source over one root in a temp directory, with a recording
// trash, so no test touches the user's.
type fixture struct {
	t       *testing.T
	base    string
	root    string
	st      *store.Store
	s       *Source
	mu      sync.Mutex
	trashed []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, base: base, root: filepath.Join(base, "root")}
	if err := os.MkdirAll(f.root, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	f.st = st
	f.s, err = New(st.Files(), []Root{{Path: f.root, Label: "notes"}}, func(p string) error {
		f.mu.Lock()
		f.trashed = append(f.trashed, p)
		f.mu.Unlock()
		return os.RemoveAll(p)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.s.Close()
		_ = st.Close()
	})
	return f
}

// path is name under the root.
func (f *fixture) path(name string) string { return filepath.Join(f.root, filepath.FromSlash(name)) }

func (f *fixture) write(name, body string) string {
	f.t.Helper()
	p := f.path(name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fixture) mkdir(name string) string {
	f.t.Helper()
	p := f.path(name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fixture) grid(dir string) *pb.GetGridResponse {
	f.t.Helper()
	resp, err := f.s.GetGrid(context.Background(), &pb.GetGridRequest{GridId: idOf(dir)})
	if err != nil {
		f.t.Fatalf("GetGrid(%s): %v", dir, err)
	}
	return resp
}

func (f *fixture) tile(path string) *pb.Tile {
	f.t.Helper()
	resp, err := f.s.GetTile(context.Background(), &pb.GetTileRequest{TileId: idOf(path)})
	if err != nil {
		f.t.Fatalf("GetTile(%s): %v", path, err)
	}
	return resp.Tile
}

func tileIn(t *testing.T, resp *pb.GetGridResponse, path string) *pb.Tile {
	t.Helper()
	for _, tl := range resp.Tiles {
		if tl.Id == idOf(path) {
			return tl
		}
	}
	t.Fatalf("no tile for %s in %v", path, resp.Tiles)
	return nil
}

func names(resp *pb.GetGridResponse) []string {
	var out []string
	for _, t := range resp.Tiles {
		out = append(out, t.AltText)
	}
	return out
}

// events subscribes and answers the stream's events as they come.
func (f *fixture) events() <-chan *pb.Event {
	f.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f.t.Cleanup(cancel)
	ch := make(chan *pb.Event, 64)
	header := make(chan struct{})
	go func() {
		_ = f.s.Subscribe(ctx, &pb.SubscribeRequest{}, func(ev *pb.Event) error {
			if ev == nil {
				close(header)
				return nil
			}
			ch <- ev
			return nil
		})
	}()
	<-header
	return ch
}

// await answers the first event match accepts, failing after a while.
func await(t *testing.T, ch <-chan *pb.Event, what string, match func(*pb.Event) bool) *pb.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if match(ev) {
				return ev
			}
		case <-deadline:
			t.Fatalf("no %s within 5s", what)
			return nil
		}
	}
}

func gridChanged(dir string) func(*pb.Event) bool {
	return func(ev *pb.Event) bool { return ev.GetGridChanged().GetGridId() == idOf(dir) }
}

func wantCode(t *testing.T, what string, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("%s: code %v (%v), want %v", what, got, err, want)
	}
}

func grpcCode(err error) codes.Code { return status.Code(err) }
