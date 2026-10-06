package sourcecache

import (
	"context"
	"path/filepath"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// stamping answers every body with a source stamp, as a far plugin's does.
type stamping struct {
	namespace.Namespace
	stamp string
}

func (s *stamping) ReadContent(ctx context.Context, in *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	first := true
	return s.Namespace.ReadContent(ctx, in, func(ch *pb.ContentChunk) error {
		if first {
			ch.ContentStamp, first = s.stamp, false
		}
		return send(ch)
	})
}

// A remembered body serves with the stamp it was read under, as it does with
// its version: a save made against a dark source's body claims that stamp
// when it lands, and a stampless replay would claim bytes nobody named.
func TestARememberedBodyKeepsItsStamp(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err := st.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	upstream := newDarkable(&stamping{Namespace: local.New(st, nil), stamp: "mtime-1"})
	cc := openLayer(t, upstream, filepath.Join(t.TempDir(), "cache.db"), Options{})

	txt, err := cc.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "text", W: 1, H: 1}, Content: []byte("words")})
	if err != nil {
		t.Fatal(err)
	}
	id := txt.GetTile().GetId()
	if got := readStamp(t, cc, id); got != "mtime-1" {
		t.Fatalf("live stamp %q, want the source's", got)
	}
	upstream.goDark()
	if got := readStamp(t, cc, id); got != "mtime-1" {
		t.Errorf("remembered stamp %q, want the one the bytes were read under", got)
	}
}

func readStamp(t *testing.T, c namespace.Namespace, tileID string) (stamp string) {
	t.Helper()
	if err := c.ReadContent(context.Background(), &pb.ReadContentRequest{TileId: tileID},
		func(ch *pb.ContentChunk) error {
			if ch.GetContentStamp() != "" {
				stamp = ch.GetContentStamp()
			}
			return nil
		}); err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	return stamp
}
