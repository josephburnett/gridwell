package pluginhost_test

import (
	"context"
	"io"
	"path/filepath"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// lackingPlugin answers Unimplemented to every optional verb, as a plugin
// that leaves them to the embedded default does, and counts each ask.
type lackingPlugin struct {
	pluginv1.UnimplementedPluginServer
	mu   sync.Mutex
	asks map[string]int
}

func (p *lackingPlugin) lack(verb string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asks[verb]++
	return status.Errorf(codes.Unimplemented, "method %s not implemented", verb)
}

func (p *lackingPlugin) count(verb string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.asks[verb]
}

func (p *lackingPlugin) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "lacking", MenuEntries: []*pluginv1.MenuEntry{{Id: "c", Context: "c"}}}, nil
}

func (p *lackingPlugin) GetPreview(context.Context, *pluginv1.GetPreviewRequest) (*pluginv1.GetPreviewResponse, error) {
	return nil, p.lack("GetPreview")
}

func (p *lackingPlugin) Probe(context.Context, *pluginv1.ProbeRequest) (*pluginv1.ProbeResponse, error) {
	return nil, p.lack("Probe")
}

func (p *lackingPlugin) Search(context.Context, *pluginv1.SearchRequest) (*pluginv1.SearchResponse, error) {
	return nil, p.lack("Search")
}

func (p *lackingPlugin) Delete(context.Context, *pluginv1.DeleteRequest) (*pluginv1.DeleteResponse, error) {
	return nil, p.lack("Delete")
}

func (p *lackingPlugin) WriteContent(pluginv1.Plugin_WriteContentServer) error {
	return p.lack("WriteContent")
}

func (p *lackingPlugin) ServeContent(*pluginv1.ServeContentRequest, pluginv1.Plugin_ServeContentServer) error {
	return p.lack("ServeContent")
}

// flipSupervisor is a process whose transitions the test drives.
type flipSupervisor struct {
	mu  sync.Mutex
	fns []func(bool, string)
}

func (s *flipSupervisor) Health() (bool, string) { return true, "" }

func (s *flipSupervisor) OnHealth(fn func(bool, string)) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fns = append(s.fns, fn)
	return func() {}
}

// respawn is the down and up of a new process.
func (s *flipSupervisor) respawn() {
	s.mu.Lock()
	fns := append([]func(bool, string){}, s.fns...)
	s.mu.Unlock()
	for _, fn := range fns {
		fn(false, "exited")
		fn(true, "")
	}
}

// absentVerbCases asks each optional verb for a tile of the lacking plugin.
var absentVerbCases = []struct {
	verb string
	ask  func(context.Context, namespace.Namespace, string) error
}{
	{"GetPreview", func(ctx context.Context, a namespace.Namespace, id string) error {
		_, err := a.GetTilePreview(ctx, &gridwellv1.GetTilePreviewRequest{TileId: id})
		return err
	}},
	{"Probe", func(ctx context.Context, a namespace.Namespace, id string) error {
		_, err := a.Probe(ctx, &gridwellv1.ProbeRequest{TileId: id})
		return err
	}},
	{"Search", func(ctx context.Context, a namespace.Namespace, _ string) error {
		_, err := a.Search(ctx, &gridwellv1.SearchRequest{Query: "x"})
		return err
	}},
	{"Delete", func(ctx context.Context, a namespace.Namespace, id string) error {
		_, err := a.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: id})
		return err
	}},
	{"WriteContent", func(ctx context.Context, a namespace.Namespace, id string) error {
		sent := false
		_, err := a.WriteContent(ctx, func() (*gridwellv1.WriteContentRequest, error) {
			if sent {
				return nil, io.EOF
			}
			sent = true
			return &gridwellv1.WriteContentRequest{TileId: id, Data: []byte("x")}, nil
		})
		return err
	}},
	{"ServeContent", func(ctx context.Context, a namespace.Namespace, id string) error {
		return a.ServeContent(ctx, &gridwellv1.ServeContentRequest{TileId: id},
			func(*gridwellv1.ServeContentChunk) error { return nil })
	}},
}

// A verb the plugin process does not implement is asked once for the life of
// that process, however many tiles want it, and the answer stays the plugin's
// Unimplemented; a respawn is a new process and is asked again.
func TestAnAbsentVerbIsAskedOncePerProcess(t *testing.T) {
	for _, tc := range absentVerbCases {
		t.Run(tc.verb, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			impl := &lackingPlugin{asks: map[string]int{}}
			cp, closer, err := plugintest.Loopback(impl)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(closer)
			sup := &flipSupervisor{}
			a := pluginhost.New(cp, st.Namespace("p1"), sup)
			ctx := t.Context()

			for _, key := range []string{"a", "b", "c", "d"} {
				if err := tc.ask(ctx, a, rpc.EntryTileID("c", key)); status.Code(err) != codes.Unimplemented {
					t.Fatalf("%s for %s = %v, want the plugin's Unimplemented", tc.verb, key, err)
				}
			}
			if got := impl.count(tc.verb); got != 1 {
				t.Fatalf("%s asked %d times of one process, want once", tc.verb, got)
			}

			sup.respawn()
			for range 2 {
				if err := tc.ask(ctx, a, rpc.EntryTileID("c", "a")); status.Code(err) != codes.Unimplemented {
					t.Fatalf("%s after a respawn = %v, want Unimplemented", tc.verb, err)
				}
			}
			if got := impl.count(tc.verb); got != 2 {
				t.Fatalf("%s asked %d times across two processes, want twice", tc.verb, got)
			}
		})
	}
}
