package server_test

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// The link-resolution seam: ReadContent and GetTilePreview on a LEAF LINK
// resolve to the target at the SERVING NODE, through the one contentRoute,
// across the node's home and a far node's and the real router. Before this,
// resolution lived only in the wasm client (rpc.ContentID): any other caller
// reading a link got an empty row.

func TestReadContentResolvesLeafLinkAtServer(t *testing.T) {
	cl, rootB, rootA := newTwoHomes(t, nil) // the target far, the link near
	ctx := context.Background()

	src, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: rootA, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 2, H: 2}}, []byte("# The Source\n\nbody"))
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	link, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootB, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 2, H: 2, LinkTargetId: src.Id, AltText: "The Source"}})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	// Reading the LINK id returns the TARGET's bytes and version — resolved
	// by the server, not by this caller.
	data, _, version, err := cl.ReadContent(ctx, link.Id)
	if err != nil {
		t.Fatalf("read through link: %v", err)
	}
	if string(data) != "# The Source\n\nbody" {
		t.Errorf("link content = %q, want the target's body", data)
	}
	if version.Version != src.Version {
		t.Errorf("link content version = %d, want the target's %d", version.Version, src.Version)
	}

	// Writing through a link id is refused — a link owns no content, and
	// content writes address the target explicitly.
	if _, err := cl.WriteContent(ctx, link.Id, rpc.ContentBasis{Version: link.Version}, []byte("stomp")); err == nil {
		t.Error("WriteContent on a link must be refused")
	}

	// The target still resolves directly, of course.
	direct, _, _, err := cl.ReadContent(ctx, src.Id)
	if err != nil || string(direct) != "# The Source\n\nbody" {
		t.Errorf("direct read: %q, %v", direct, err)
	}
}

func TestPreviewReadResolvesLeafLink(t *testing.T) {
	cl, rootB, rootA := newTwoHomes(t, nil) // the target far, the link near
	ctx := context.Background()

	// A url tile in the far home with a frozen JPEG preview.
	src, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootA, Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 0, Y: 0, W: 2, H: 2, UrlString: "https://example.com"}})
	if err != nil {
		t.Fatalf("create url: %v", err)
	}
	jpeg := []byte("\xff\xd8fake-jpeg-bytes")
	if _, err := cl.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: src.Id, Tile: &gridwellv1.Tile{Kind: rpc.KindURL}, Preview: jpeg}); err != nil {
		t.Fatalf("freeze: %v", err)
	}

	link, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootB, Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 0, Y: 0, W: 2, H: 2, LinkTargetId: src.Id, AltText: "example"}})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	// The RPC route: the link's preview is the target's frozen JPEG.
	got, err := cl.GetTilePreview(ctx, link.Id)
	if err != nil {
		t.Fatalf("preview through link: %v", err)
	}
	if string(got) != string(jpeg) {
		t.Errorf("link preview = %q, want the target's jpeg", got)
	}

}
