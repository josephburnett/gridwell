package server

// The build gate, crossed for real: the wasm client's own carriers (the
// nodebuild interceptor on the Connect client, the beacon path, shellws) as a
// page of the door's origin, against WebHandler with a stamped build.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/nodebuild"
	"github.com/josephburnett/gridwell/client/shellstream"
	"github.com/josephburnett/gridwell/client/shellws"
)

const gateNodeBuild = "node-b"

// asPage is the header a page's own calls carry: its origin is the door's.
type asPage struct{ origin string }

func (a asPage) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Origin", a.origin)
		return next(ctx, req)
	}
}

func (a asPage) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Origin", a.origin)
		return conn
	}
}

func (a asPage) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func gateFixture(t *testing.T, nodeBuild string) *shellDoorFixture {
	return newShellDoorFixture(t, Config{}, func(s *Server) { s.build = nodeBuild })
}

// page is a client of the given build, and what its gate heard.
func (f *shellDoorFixture) page(build string) (*rpc.Client, *nodebuild.Gate, *[]string) {
	heard := &[]string{}
	g := nodebuild.New(build, func(node string) { *heard = append(*heard, node) })
	cl := rpc.NewClient(f.hs.Client(), f.hs.URL, connect.WithProtoJSON(),
		connect.WithInterceptors(asPage{f.hs.URL}, g.Interceptor()))
	return cl, g, heard
}

func (f *shellDoorFixture) tileCount(t *testing.T) int {
	t.Helper()
	g, err := f.cl.GetGrid(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	return len(g.Tiles)
}

func TestBuildGateRefusesAPageOfAnotherBuildBeforeTheVerb(t *testing.T) {
	for _, pageBuild := range []string{"page-a", ""} {
		f := gateFixture(t, gateNodeBuild)
		cl, g, heard := f.page(pageBuild)
		_, err := cl.CreateTile(context.Background(), &gridwellv1.CreateTileRequest{GridId: f.root,
			Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}})
		if node, ok := gwerr.StaleBuildOf(err); !ok || node != gateNodeBuild {
			t.Fatalf("page %q: CreateTile err = %v, want the stale-build verdict naming %q", pageBuild, err, gateNodeBuild)
		}
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Errorf("page %q: code %v, want FailedPrecondition", pageBuild, connect.CodeOf(err))
		}
		if len(*heard) != 1 || g.Accepted() {
			t.Errorf("page %q: gate heard %v accepted %v", pageBuild, *heard, g.Accepted())
		}
		if n := f.tileCount(t); n != 0 {
			t.Errorf("page %q: the verb ran: %d tiles", pageBuild, n)
		}
	}
}

func TestBuildGatePassesTheNodesOwnBuild(t *testing.T) {
	f := gateFixture(t, gateNodeBuild)
	cl, g, heard := f.page(gateNodeBuild)
	if _, err := cl.Handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*heard) != 0 || !g.Accepted() {
		t.Errorf("heard %v accepted %v", *heard, g.Accepted())
	}
}

// A caller with no Origin is no page, and an unstamped node judges nothing.
func TestBuildGateJudgesOnlyAStampedNodesPages(t *testing.T) {
	f := gateFixture(t, gateNodeBuild)
	if _, err := f.cl.Handshake(context.Background()); err != nil {
		t.Errorf("a tool with no Origin: %v", err)
	}
	u := gateFixture(t, "")
	cl, _, heard := u.page("page-a")
	if _, err := cl.Handshake(context.Background()); err != nil || len(*heard) != 0 {
		t.Errorf("unstamped node: err %v heard %v", err, *heard)
	}
}

func TestBuildGateRefusesTheStream(t *testing.T) {
	f := gateFixture(t, gateNodeBuild)
	cl, _, heard := f.page("page-a")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := cl.Subscribe(ctx)
	if err == nil {
		defer stream.Close()
		_, _, err = stream.Recv()
	}
	if node, ok := gwerr.StaleBuildOf(err); !ok || node != gateNodeBuild {
		t.Fatalf("Subscribe err = %v, want the stale-build verdict", err)
	}
	if len(*heard) != 1 {
		t.Errorf("gate heard %v", *heard)
	}
}

// A beacon names its build in the query; a stale one is refused like a call,
// in the Connect form a beacon's unary JSON post reads.
func TestBuildGateRefusesAStaleBeacon(t *testing.T) {
	f := gateFixture(t, gateNodeBuild)
	for _, tc := range []struct {
		build string
		want  int
		stale bool
	}{{"page-a", http.StatusBadRequest, true}, {gateNodeBuild, http.StatusOK, false}} {
		path, body := rpc.SetFramingBeacon(&gridwellv1.SetFramingRequest{RootGridId: f.root, Cx: 1, Cy: 2, Zoom: 0.5})
		req, err := http.NewRequest(http.MethodPost, f.hs.URL+nodebuild.New(tc.build, nil).BeaconPath(path), bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", rpc.BeaconJSONType)
		req.Header.Set("Origin", f.hs.URL)
		res, err := f.hs.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != tc.want || bytes.Contains(got, []byte("gridwell.v1.StaleBuild")) != tc.stale {
			t.Errorf("beacon of %q = %d %s, want %d stale=%v", tc.build, res.StatusCode, got, tc.want, tc.stale)
		}
	}
}

// The socket's browser end cannot read an HTTP status, so the verdict is an
// exit frame, and no PTY is touched.
func TestBuildGateRefusesTheShellSocket(t *testing.T) {
	f := gateFixture(t, gateNodeBuild)
	tile := f.createShell(t, 0, 0)
	stale := make(chan string, 1)
	exit := make(chan shellstream.Exit, 1)
	dial := shellws.Dialer(shellws.Options{
		Origin:       f.hs.URL,
		HTTPClient:   f.hs.Client(),
		Header:       http.Header{"Origin": {f.hs.URL}},
		Build:        "page-a",
		OnStaleBuild: func(node string) { stale <- node },
	})
	reg := shellstream.New(dial, func(string, []byte) {}, func(e shellstream.Exit) { exit <- e })
	reg.Open("k", tile.Id, 80, 24)
	select {
	case node := <-stale:
		if node != gateNodeBuild {
			t.Errorf("socket verdict names %q, want the node's build", node)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stale socket never heard the verdict")
	}
	select {
	case <-exit:
	case <-time.After(5 * time.Second):
		t.Fatal("the stale socket never ended")
	}
	if f.fake.SessionCount() != 0 {
		t.Error("a stale page's socket touched the PTY")
	}
}
