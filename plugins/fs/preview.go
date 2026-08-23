package fs

// The frozen face of an image tile (2026-08-11): GetTilePreview decodes the
// file and returns a small JPEG, so a serves_page image presents like a
// frozen url tile. Stateless and derived: the file is the one owner. The
// decode/downscale core lives in fsfile, shared with the v2 provider.

import (
	"context"
	"strconv"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/plugins/fs/fsfile"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetTilePreview serves a JPEG thumbnail for image file tiles. Non-image
// tiles (and undecodable files) return an empty preview, never an error —
// the client falls back to the label exactly as it does for a url tile
// with no capture yet.
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
	if kind != "text" {
		return &gridwellv1.GetTilePreviewResponse{}, nil
	}
	dirPath, err := p.gridPath(gridID)
	if err != nil {
		return &gridwellv1.GetTilePreviewResponse{}, nil
	}
	return &gridwellv1.GetTilePreviewResponse{Jpeg: fsfile.PreviewJPEG(dirPath, name)}, nil
}
