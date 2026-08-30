package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// The link-resolution seam (owner decision 8, 2026-07-26): ReadContent and
// GetTilePreview on a LEAF LINK resolve to the target at the SERVING NODE,
// through the one contentRoute door — across two real plugins and the real
// router. Before this, resolution lived only in the wasm client
// (rpc.Tile.ContentID): any other caller reading a link got an empty row.

// twoPluginHTTPServer is twoPluginServer plus the raw HTTP base URL, for the
// /preview/tile/ door.
func twoPluginHTTPServer(t *testing.T) (cl *rpc.Client, baseURL, rootA, rootB string) {
	t.Helper()
	ctx := context.Background()
	reg := plugin.NewRegistry()

	stA, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stA.Close() })
	_, rootA = registerPrimaryLocaldb(t, reg, stA)

	stB, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stB.Close() })
	uuidB, err := stB.PluginUUID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clientB := local.New(stB, nil)
	reg.Register(uuidB, "home", clientB, nil)
	bareRootB, err := stB.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}

	srv := mustNew(t, reg, Config{})
	hs := serveWeb(t, srv)
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON()), hs.URL, rootA, uuidB + "/" + bareRootB
}

func TestReadContentResolvesLeafLinkAtServer(t *testing.T) {
	cl, _, rootA, rootB := twoPluginHTTPServer(t)
	ctx := context.Background()

	src, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: rootA, X: 0, Y: 0, W: 2, H: 2, Data: []byte("# The Source\n\nbody"),
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	link, err := cl.CreateLeafLink(ctx, &rpc.CreateLeafLinkRequest{
		GridID: rootB, X: 0, Y: 0, W: 2, H: 2, Kind: rpc.KindText,
		LinkTargetID: src.ID, Label: "The Source",
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	// Reading the LINK id returns the TARGET's bytes and version — resolved
	// by the server, not by this caller.
	data, _, version, err := cl.ReadContent(ctx, link.ID)
	if err != nil {
		t.Fatalf("read through link: %v", err)
	}
	if string(data) != "# The Source\n\nbody" {
		t.Errorf("link content = %q, want the target's body", data)
	}
	if version != src.Version {
		t.Errorf("link content version = %d, want the target's %d", version, src.Version)
	}

	// Writing through a link id is refused — a link owns no content, and
	// content writes address the target explicitly.
	if _, err := cl.WriteContent(ctx, link.ID, link.Version, []byte("stomp")); err == nil {
		t.Error("WriteContent on a link must be refused")
	}

	// The target still resolves directly, of course.
	direct, _, _, err := cl.ReadContent(ctx, src.ID)
	if err != nil || string(direct) != "# The Source\n\nbody" {
		t.Errorf("direct read: %q, %v", direct, err)
	}
}

func TestPreviewDoorResolvesLeafLink(t *testing.T) {
	cl, _, rootA, rootB := twoPluginHTTPServer(t)
	ctx := context.Background()

	// A url tile in A with a frozen JPEG preview.
	src, err := cl.CreateURL(ctx, &rpc.CreateURLRequest{
		GridID: rootA, X: 0, Y: 0, W: 2, H: 2, URL: "https://example.com",
	})
	if err != nil {
		t.Fatalf("create url: %v", err)
	}
	jpeg := []byte("\xff\xd8fake-jpeg-bytes")
	if _, err := cl.SetURLState(ctx, &rpc.SetURLStateRequest{
		TileID: src.ID, Version: src.Version, JPEG: jpeg,
	}); err != nil {
		t.Fatalf("freeze: %v", err)
	}

	link, err := cl.CreateLeafLink(ctx, &rpc.CreateLeafLinkRequest{
		GridID: rootB, X: 0, Y: 0, W: 2, H: 2, Kind: rpc.KindURL,
		LinkTargetID: src.ID, Label: "example",
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	// The RPC door: the link's preview is the target's frozen JPEG.
	got, err := cl.GetTilePreview(ctx, link.ID)
	if err != nil {
		t.Fatalf("preview through link: %v", err)
	}
	if string(got) != string(jpeg) {
		t.Errorf("link preview = %q, want the target's jpeg", got)
	}

}
