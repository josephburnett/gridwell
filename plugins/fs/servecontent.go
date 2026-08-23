package fs

// The fs plugin's web-content side (2026-08-11): any file tile can be
// fetched raw through the /content/ door, and files a browser can present
// natively — images, HTML, audio, video — additionally declare serves_page,
// so the client gives their descent url-tile semantics (a live view of the
// image, not a metadata summary). ReadContent is untouched: it remains the
// small DOCUMENT body; this door is the file itself.
//
// The pure derivations and the streaming core live in plugins/fs/fsfile,
// SHARED with the v2 fs content provider so the two answer identically by
// construction (the migration parity gate leans on that); this file keeps
// only the tile-id → path resolution the legacy DB owns.

import (
	"strconv"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/plugins/fs/fsfile"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// stampServesPage derives the serves_page bit onto loaded tile rows — from
// the one fact that owns it, the filename (AltText carries it; fs's label
// column). Derived at read time, never stored. dirPath, when non-empty,
// lets IMAGE tiles carry a preview GENERATION in preview_blob_id (the
// file's mtime): the client keys its thumbnail cache by that field, so an
// edited image invalidates naturally instead of showing last session's
// face forever.
func stampServesPage(tiles []*gridwellv1.Tile, dirPath string) {
	for _, t := range tiles {
		if t.Kind != "text" {
			continue
		}
		t.ServesPage = fsfile.ServesPage(t.AltText)
		t.TextPresentation = fsfile.TextPresentation(t.AltText)
		if stamp := fsfile.PreviewStamp(dirPath, t.AltText); stamp != 0 {
			t.PreviewBlobId = stamp
		}
	}
}

// ServeContent streams a file's raw bytes as web content. subpath "" is the
// tile's own file; a non-empty subpath is a page-relative resource resolved
// against the file's directory (fsfile.ServeFile owns the confinement).
func (p *Plugin) ServeContent(req *gridwellv1.ServeContentRequest, stream grpc.ServerStreamingServer[gridwellv1.ServeContentChunk]) error {
	tileID, err := strconv.ParseInt(req.TileId, 10, 64)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "fs ServeContent: invalid tile_id %q", req.TileId)
	}
	var gridID int64
	var name, kind string
	err = p.db.QueryRow(`SELECT grid_id, name, kind FROM tiles WHERE id = ?`, tileID).Scan(&gridID, &name, &kind)
	if err != nil {
		return status.Errorf(codes.NotFound, "fs ServeContent: no tile %d", tileID)
	}
	if kind != "text" {
		return status.Error(codes.NotFound, "fs ServeContent: directories serve no page")
	}
	dirPath, err := p.gridPath(gridID)
	if err != nil {
		return err
	}
	return fsfile.ServeFile(stream, dirPath, name, req.Subpath)
}
