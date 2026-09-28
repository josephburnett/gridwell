package pluginhost_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// goneSource lists "doc" until it is told the key is gone, and says so the
// way the case under test says: an authoritative listing that omits it, a
// probe, or a directory that cannot be read at all.
type goneSource struct {
	pluginv1.UnimplementedPluginServer

	mu            sync.Mutex
	listed        bool
	authoritative bool
	dark          bool
	presence      pluginv1.ProbeResponse_Presence
}

func (p *goneSource) set(edit func(*goneSource)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	edit(p)
}

func (p *goneSource) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "goneish", DisplayName: "goneish",
		MenuEntries: []*pluginv1.MenuEntry{{Id: "r", Context: "r"}}}, nil
}

func (p *goneSource) List(context.Context, *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dark {
		return nil, status.Error(codes.Unavailable, "source dark")
	}
	resp := &pluginv1.ListResponse{Authoritative: p.authoritative,
		Entries: []*pluginv1.Entry{{Key: "other", Kind: "text", Label: "other"}}}
	if p.listed {
		resp.Entries = append(resp.Entries, &pluginv1.Entry{Key: "doc", Kind: "text", Label: "doc"})
	}
	return resp, nil
}

func (p *goneSource) Probe(context.Context, *pluginv1.ProbeRequest) (*pluginv1.ProbeResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return &pluginv1.ProbeResponse{Presence: p.presence}, nil
}

func (p *goneSource) ReadContent(req *pluginv1.ReadContentRequest, s pluginv1.Plugin_ReadContentServer) error {
	return s.Send(&pluginv1.ContentChunk{Data: []byte("body of " + req.Key)})
}

func (p *goneSource) GetPreview(context.Context, *pluginv1.GetPreviewRequest) (*pluginv1.GetPreviewResponse, error) {
	return &pluginv1.GetPreviewResponse{}, nil
}

// A key the source says is gone reads as dead through every read a link's
// target takes, and only then: a source that is dark, or that lists without
// authority and cannot say the key is gone, is not a verdict on the key. The
// same key listed again reads again under the same id, because a link is never
// rewritten.
func TestAGoneKeyReadsDeadAndComesBack(t *testing.T) {
	const (
		dead  = "dead"
		live  = "live"
		other = "an error that is not the dead verdict"
	)
	for _, tc := range []struct {
		name          string
		authoritative bool
		dark          bool
		presence      pluginv1.ProbeResponse_Presence
		untouched     string // what the reads answer when the entry has no row
		minted        string // and when a durable touch minted one first
	}{
		{"an authoritative listing omits it", true, false, 0, dead, dead},
		{"a partial listing omits it and the probe says gone", false, false, pluginv1.ProbeResponse_PRESENCE_GONE, dead, dead},
		{"a partial listing omits it and the probe says present", false, false, pluginv1.ProbeResponse_PRESENCE_PRESENT, other, live},
		{"a partial listing omits it and the probe cannot say", false, false, pluginv1.ProbeResponse_PRESENCE_UNSPECIFIED, other, live},
		{"the source is dark", true, true, 0, other, live},
	} {
		for _, mint := range []bool{false, true} {
			want := tc.untouched
			name := tc.name + "/untouched"
			if mint {
				want, name = tc.minted, tc.name+"/minted"
			}
			t.Run(name, func(t *testing.T) {
				src := &goneSource{listed: true, authoritative: true}
				cl, landing := goneStack(t, src)
				ctx := context.Background()
				target := tileLabelled(ctx, t, cl, landing, "doc")
				if mint {
					if _, err := cl.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: target, X: 4, Y: 4, W: 1, H: 1}); err != nil {
						t.Fatal(err)
					}
				}

				src.set(func(p *goneSource) {
					p.listed, p.authoritative, p.dark, p.presence = false, tc.authoritative, tc.dark, tc.presence
				})
				// A visit lists the grid first, and whatever that listing
				// retires is what the reads after it must agree with.
				_, _ = cl.GetGrid(ctx, landing)
				reads := map[string]error{}
				_, reads["GetTile"] = cl.GetTile(ctx, target)
				_, _, _, reads["ReadContent"] = cl.ReadContent(ctx, target)
				_, reads["GetTilePreview"] = cl.GetTilePreview(ctx, target)
				for verb, err := range reads {
					switch want {
					case dead:
						if !gwerr.IsDeadRef(err) {
							t.Errorf("%s = %v, want the dead verdict", verb, err)
						}
					case live:
						if err != nil {
							t.Errorf("%s = %v, want the remembered tile", verb, err)
						}
					default:
						if err == nil || gwerr.IsDeadRef(err) {
							t.Errorf("%s = %v, want %s", verb, err, other)
						}
					}
				}
				if tc.dark && want != live {
					if _, err := cl.GetTile(ctx, target); connect.CodeOf(err) != connect.CodeUnavailable {
						t.Errorf("an unremembered key under a dark source = %v, want the transport-class answer", err)
					}
				}

				src.set(func(p *goneSource) { p.listed, p.authoritative, p.dark = true, true, false })
				if _, err := cl.GetTile(ctx, target); err != nil {
					t.Fatalf("the key is listed again and its id still reads %v", err)
				}
				if b, _, _, err := cl.ReadContent(ctx, target); err != nil || string(b) != "body of doc" {
					t.Fatalf("the key is listed again and reads (%q, %v)", b, err)
				}
			})
		}
	}
}

// A row retired because its key went is dead under the row id a reference
// stored under the older rule still holds.
func TestARetiredRowReadsDead(t *testing.T) {
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	src := &goneSource{listed: true, authoritative: true}
	cp, closer, err := plugintest.Loopback(src)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	ns := memStore.Namespace("p1")
	a := pluginhost.New(cp, ns, nil)
	ctx := context.Background()
	info, err := a.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	landing := plugintest.Landing(t, info)
	g, err := a.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: landing})
	if err != nil {
		t.Fatal(err)
	}
	var addr string
	for _, tile := range g.Tiles {
		if tile.AltText == "doc" {
			addr = tile.Id
		}
	}
	if _, err := a.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: addr, X: 4, Y: 4, W: 1, H: 1}); err != nil {
		t.Fatal(err)
	}
	row := rowIDOf(t, ns, "r", "doc")
	src.set(func(p *goneSource) { p.listed = false })
	if _, err := a.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: landing}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: row}); !gwerr.IsDeadRef(err) {
		t.Fatalf("GetTile on the retired row = %v, want the dead verdict", err)
	}
}

// goneStack serves src behind the production router and answers a client and
// the landing grid's qualified id.
func goneStack(t *testing.T, src *goneSource) (*rpc.Client, string) {
	t.Helper()
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp, closer, err := plugintest.Loopback(src)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register("gonesrc", "goneish", pluginhost.New(cp, memStore.Namespace("p1"), nil), nil)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	pl, err := cl.Handshake(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cl, plugintest.LandingOf(t, pl.Plugins[0])
}

func tileLabelled(ctx context.Context, t *testing.T, cl *rpc.Client, gridID, label string) string {
	t.Helper()
	g, err := cl.GetGrid(ctx, gridID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tile := range g.Tiles {
		if tile.AltText == label {
			return tile.Id
		}
	}
	t.Fatalf("no %q in %v", label, g.Tiles)
	return ""
}
