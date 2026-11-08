package server_test

// A far node's files through a connection: the id "<node>/<conn>/<far>/~…"
// forwards through the node's transport one hop per segment, as every other
// id does.

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
)

// farFileID is the id the node answers for a far node's file at path,
// through the connection "geneva".
func farFileID(path string) string {
	return localNodeID + "/geneva/" + rpc.QualifyID("rnode1", rpc.KeyTileID(path))
}

// A far node's image is a page the content API streams back through the
// connection, so a connection carries pages as it does everything else.
func TestContentAPIThroughAConnection(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(dir, "remote.png")
	if err := os.WriteFile(img, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil, filesAt(t, "rnode1", dir))
	lp, err := h.localCl.Handshake(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.localCl.GetGrid(t.Context(), farFileID(dir)); err != nil {
		t.Fatalf("the far directory through the connection: %v", err)
	}
	res, err := http.Get(rpc.PageURL(h.localURL, lp.ContentToken, farFileID(img)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !bytes.Equal(body, buf.Bytes()) || res.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("connection GET = %d %q, %d bytes; want the remote image's %d", res.StatusCode, res.Header.Get("Content-Type"), len(body), buf.Len())
	}
}
