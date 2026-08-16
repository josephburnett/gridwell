package fs

// The frozen face of an image tile (2026-08-11): GetTilePreview decodes the
// file and returns a small JPEG, so a serves_page image presents like a
// frozen url tile — a real picture in the grid and in a browser-host pane,
// not a metadata summary. Stateless and derived: the file is the one owner;
// nothing is cached or stored (fs stays a projection).

import (
	"bytes"
	"context"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"strconv"
	"strings"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// previewMaxEdge bounds the thumbnail: previews are small tiles, and the
	// full image is always one descent away through the /content/ door.
	previewMaxEdge = 512
	// previewFileCap skips absurdly large files rather than decode them on
	// every grid paint. Past it the tile falls back to its label, like a url
	// tile with no capture yet.
	previewFileCap = 64 << 20
)

// GetTilePreview serves a JPEG thumbnail for image file tiles. Non-image
// tiles (and undecodable files) return an empty preview, never an error —
// the client falls back to the label exactly as it does for a url tile
// with no capture.
func (p *Plugin) GetTilePreview(_ context.Context, req *gridwellv1.GetTilePreviewRequest) (*gridwellv1.GetTilePreviewResponse, error) {
	tileID, err := strconv.ParseInt(req.TileId, 10, 64)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "fs GetTilePreview: invalid tile_id %q", req.TileId)
	}
	var gridID int64
	var name, kind string
	if err := p.db.QueryRow(`SELECT grid_id, name, kind FROM tiles WHERE id = ?`, tileID).Scan(&gridID, &name, &kind); err != nil {
		return &gridwellv1.GetTilePreviewResponse{}, nil
	}
	if kind != "text" || !strings.HasPrefix(pageMediaType(name), "image/") {
		return &gridwellv1.GetTilePreviewResponse{}, nil
	}
	dirPath, err := p.gridPath(gridID)
	if err != nil {
		return &gridwellv1.GetTilePreviewResponse{}, nil
	}
	full := dirPath + string(os.PathSeparator) + name
	if fi, err := os.Stat(full); err != nil || fi.Size() > previewFileCap {
		return &gridwellv1.GetTilePreviewResponse{}, nil
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return &gridwellv1.GetTilePreviewResponse{}, nil
	}
	jpg := thumbnailJPEG(data)
	return &gridwellv1.GetTilePreviewResponse{Jpeg: jpg}, nil
}

// thumbnailJPEG decodes any stdlib-supported image (png, jpeg, gif) and
// re-encodes a bounded JPEG. Undecodable input (webp, svg, a corrupt file)
// returns nil — the caller treats that as "no preview".
func thumbnailJPEG(data []byte) []byte {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	img = downscale(img, previewMaxEdge)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil
	}
	return out.Bytes()
}

// downscale bounds the longest edge to maxEdge with nearest-neighbor
// sampling — a preview, not an archival resize; stdlib-only on purpose.
func downscale(img image.Image, maxEdge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxEdge && h <= maxEdge {
		return img
	}
	scale := float64(maxEdge) / float64(w)
	if h > w {
		scale = float64(maxEdge) / float64(h)
	}
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy := b.Min.Y + y*h/nh
		for x := 0; x < nw; x++ {
			sx := b.Min.X + x*w/nw
			dst.Set(x, y, img.At(sx, sy))
		}
	}
	return dst
}
