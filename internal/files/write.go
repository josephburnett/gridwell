package files

// The writes the node keeps on a file's row, and the two the disk takes: a
// move renames the file, a delete trashes it.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// MoveTile writes a placement on the file's row, minting it. A move to
// another directory renames the file there first, and the row follows under
// its new path, keeping its id; a move to another source is refused.
func (s *Source) MoveTile(_ context.Context, req *pb.MoveTileRequest) (*pb.TileResponse, error) {
	path, dir, err := s.tilePath(req.TileId)
	if err != nil {
		return nil, err
	}
	to := dir
	if req.GridId != "" {
		if rpc.ShapeOf(req.GridId) != rpc.ShapeKey {
			return nil, status.Error(codes.InvalidArgument, "files: a move to another source is refused")
		}
		if to, err = s.dirOf(req.GridId); err != nil {
			return nil, err
		}
	}
	if to != dir {
		return s.moveAcross(req, path, dir, to)
	}
	id, err := s.mint(req.TileId)
	if err != nil {
		return nil, err
	}
	changed, err := s.mem.Move(id, 0, "", req.X, req.Y, req.W, req.H, req.Claim)
	return s.changedTile(path, changed, err, true)
}

// moveAcross renames path into to. The claim is checked before the rename,
// since a rename is not undone.
func (s *Source) moveAcross(req *pb.MoveTileRequest, path, dir, to string) (*pb.TileResponse, error) {
	t, err := s.tileByPath(req.TileId)
	if err != nil {
		return nil, err
	}
	if err := store.ClaimPlacement(t, req.Claim, false); err != nil {
		return nil, gwerr.ToStatus(err)
	}
	id, err := s.mint(req.TileId)
	if err != nil {
		return nil, err
	}
	toGid, err := s.mem.GridOf(to)
	if err != nil {
		return nil, err
	}
	landed, changed, err := func() (string, bool, error) {
		s.moving.Lock()
		defer s.moving.Unlock()
		landed, err := s.rename(path, to)
		if err != nil {
			return "", false, err
		}
		changed, err := s.mem.Move(id, toGid, landed, req.X, req.Y, req.W, req.H, req.Claim)
		return landed, changed, err
	}()
	if err != nil {
		return nil, gwerr.ToStatus(err)
	}
	s.emitGridChanged(dir)
	return s.changedTile(landed, changed, nil, true)
}

// rename moves path into the directory to under its name and answers where
// it landed. It replaces nothing.
func (s *Source) rename(path, to string) (string, error) {
	refuse := func(c codes.Code, why string, a ...any) error {
		return status.Errorf(c, "files: %s cannot move to %s: "+why, append([]any{path, to}, a...)...)
	}
	fi, err := os.Stat(to)
	switch {
	case gone(err):
		return "", refuse(codes.FailedPrecondition, "that directory is gone")
	case err != nil:
		return "", refuse(codes.FailedPrecondition, "%v", pathErr(err))
	case !fi.IsDir():
		return "", refuse(codes.InvalidArgument, "that is not a directory")
	case underRoot(path, to):
		return "", refuse(codes.InvalidArgument, "a directory cannot move into itself")
	}
	if _, err := os.Lstat(path); err != nil {
		return "", refuse(codes.FailedPrecondition, "it is gone")
	}
	landed := filepath.Join(to, filepath.Base(path))
	_, err = os.Lstat(landed)
	switch {
	case err == nil:
		return "", refuse(codes.FailedPrecondition, "%q is already there", filepath.Base(path))
	case gone(err):
		err = os.Rename(path, landed)
	}
	if err != nil {
		return "", refuse(codes.FailedPrecondition, "%v", pathErr(err))
	}
	return landed, nil
}

// SetTile writes one operation on the file's row, minting it: a rename is the
// user's name, kept on the row, and the file is not renamed; a url
// writeback keeps only its screenshot, since a page file has no address.
func (s *Source) SetTile(_ context.Context, req *pb.SetTileRequest) (*pb.TileResponse, error) {
	if why := rpc.OneOperation(req); why != "" {
		return nil, status.Error(codes.InvalidArgument, why)
	}
	if t := req.GetTile(); rpc.KindOf(t.GetKind()).Content == rpc.ContentAddress {
		if t.UrlString != "" || t.AltText != "" || t.UrlHistory != "" {
			return nil, status.Error(codes.InvalidArgument,
				"files: a page file's title and history are its own and it has no address; the writeback carries the screenshot alone")
		}
		if len(req.Preview) == 0 {
			return nil, status.Error(codes.InvalidArgument, "files: a url writeback with no screenshot writes nothing")
		}
	}
	var zoom rpc.ContentZoom
	if req.ContentZoom != nil {
		z, err := rpc.NewContentZoom(*req.ContentZoom)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		zoom = z
	}
	path, _, err := s.tilePath(req.TileId)
	if err != nil {
		return nil, err
	}
	id, err := s.mint(req.TileId)
	if err != nil {
		return nil, err
	}
	var changed bool
	switch {
	case req.Rename != "":
		changed, err = s.mem.Rename(id, req.Version, req.Rename)
	case req.ContentZoom != nil:
		changed, err = s.mem.SetContentZoom(id, zoom)
	case req.UrlFrozen != nil:
		changed, err = s.mem.SetFrozen(id, *req.UrlFrozen, req.ClaimFrozen)
	default:
		t := req.GetTile()
		switch rpc.KindOf(t.GetKind()).Content {
		case rpc.ContentBody:
			changed, err = s.mem.SetTextScroll(id, t.GetTextX(), t.GetTextY(), t.GetTextW(), t.GetTextH(), t.GetTextMode())
		case rpc.ContentAddress:
			changed, err = s.mem.SetURLPreview(id, req.Preview)
		default:
			return nil, status.Errorf(codes.InvalidArgument, "files: unsupported SetTile kind %q", t.GetKind())
		}
	}
	return s.changedTile(path, changed, err, false)
}

// changedTile reads back a write to path's row and, when it changed the row,
// announces it: a placement is part of its directory's listing, so a move
// announces the grid; any other write announces the tile.
func (s *Source) changedTile(path string, changed bool, err error, placed bool) (*pb.TileResponse, error) {
	if err != nil {
		return nil, gwerr.ToStatus(err)
	}
	t, err := s.tileByPath(idOf(path))
	if err != nil {
		return nil, err
	}
	switch {
	case changed && placed:
		s.emitGridChanged(filepath.Dir(path))
	case changed:
		s.hub.Publish(&pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: t}}})
	}
	return &pb.TileResponse{Tile: t}, nil
}

// SetFraming writes a root's framing on its directory's grid row and any
// other framing on the door's row.
func (s *Source) SetFraming(_ context.Context, req *pb.SetFramingRequest) (*pb.SetFramingResponse, error) {
	f, err := rpc.FramingOf(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if req.RootGridId != "" {
		dir, err := s.dirOf(req.RootGridId)
		if err != nil {
			return nil, err
		}
		gid, err := s.mem.GridOf(dir)
		if err != nil {
			return nil, err
		}
		changed, err := s.mem.SetFraming(0, gid, f)
		if err != nil {
			return nil, gwerr.ToStatus(err)
		}
		if changed {
			s.hub.Publish(rpc.FramingEvent(idOf(dir), f))
		}
		return &pb.SetFramingResponse{}, nil
	}
	path, _, err := s.tilePath(req.TileId)
	if err != nil {
		return nil, err
	}
	id, err := s.mint(req.TileId)
	if err != nil {
		return nil, err
	}
	changed, err := s.mem.SetFraming(id, 0, f)
	resp, err := s.changedTile(path, changed, err, false)
	if err != nil {
		return nil, err
	}
	return &pb.SetFramingResponse{Tile: resp.Tile}, nil
}

// SetSelection keeps a directory's selection on its grid row, as home keeps
// a grid's. An added file gets its row; one not in the directory is ignored.
func (s *Source) SetSelection(_ context.Context, req *pb.SetSelectionRequest) (*pb.SetSelectionResponse, error) {
	if why := rpc.SelectRefusal(req); why != "" {
		return nil, status.Error(codes.InvalidArgument, "files: "+why)
	}
	if req.All {
		changed, err := s.mem.ClearAllSelections()
		if err != nil {
			return nil, gwerr.ToStatus(err)
		}
		return &pb.SetSelectionResponse{}, s.announceSelections(changed)
	}
	dir, err := s.dirOf(req.GridId)
	if err != nil {
		return nil, err
	}
	var add, remove []int64
	for _, id := range req.Add {
		_, in, err := s.tilePath(id)
		if errGone(err) || err == nil && in != dir {
			continue
		}
		if err != nil {
			return nil, err
		}
		row, err := s.mint(id)
		if errGone(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		add = append(add, row)
	}
	for _, id := range req.Remove {
		path, in, err := s.tilePath(id)
		if errGone(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if row, ok, err := s.row(in, path); err != nil {
			return nil, err
		} else if ok {
			remove = append(remove, row)
		}
	}
	gid, err := s.mem.GridOf(dir)
	if err != nil {
		return nil, err
	}
	changed, err := s.mem.SetSelection(gid, store.SelectOp{Add: add, Remove: remove, Clear: req.Clear, Recurse: req.Recurse})
	if err != nil {
		return nil, gwerr.ToStatus(err)
	}
	return &pb.SetSelectionResponse{}, s.announceSelections(changed)
}

// announceSelections tells each grid's whole set.
func (s *Source) announceSelections(grids []int64) error {
	for _, g := range grids {
		dir, err := s.mem.GridKey(g)
		if err != nil {
			return err
		}
		ids, err := s.selection(g)
		if err != nil {
			return err
		}
		s.hub.Publish(rpc.SelectionEvent(idOf(dir), ids))
	}
	return nil
}

// Probe answers from the disk whether the path is there.
func (s *Source) Probe(_ context.Context, req *pb.ProbeRequest) (*pb.ProbeResponse, error) {
	presence := pb.ProbeResponse_PRESENCE_UNSPECIFIED
	path, err := pathOf(req.TileId)
	if err == nil {
		err = s.inTree(path)
	}
	if err == nil {
		_, err = os.Lstat(path)
	}
	switch {
	case err == nil:
		presence = pb.ProbeResponse_PRESENCE_PRESENT
	case gone(err), errors.Is(err, errOutside), status.Code(err) == codes.InvalidArgument:
		presence = pb.ProbeResponse_PRESENCE_GONE
	}
	return &pb.ProbeResponse{Presence: presence}, nil
}

// DeleteTile moves the file to the trash, a link itself and never what it
// points at, and retires its row once the path is gone. A path already gone
// is deleted.
func (s *Source) DeleteTile(_ context.Context, req *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	path, err := pathOf(req.TileId)
	if err != nil {
		return nil, err
	}
	switch err := s.inTree(path); {
	case errors.Is(err, errOutside):
		return nil, status.Errorf(codes.InvalidArgument, "files: %s %v, and nothing there is deleted", path, err)
	case err != nil:
		return nil, s.refuse(path, err)
	case s.isRoot(path):
		return nil, status.Errorf(codes.InvalidArgument, "files: %s is a root, which is config and is not deleted", path)
	}
	dir := filepath.Dir(path)
	if req.GetClaim() != nil {
		// A file already gone has nothing left a claim could lose.
		t, err := s.tileByPath(req.TileId)
		switch {
		case gwerr.IsDeadLink(err):
		case err != nil:
			return nil, err
		default:
			if err := store.DeleteClaim(t, req, false); err != nil {
				return nil, gwerr.ToStatus(err)
			}
		}
	}
	_, err = os.Lstat(path)
	switch {
	case gone(err):
	case err != nil:
		return nil, status.Errorf(codes.Unavailable, "files: delete %s: %v", path, pathErr(err))
	default:
		if err := s.trash(path); err != nil {
			return nil, status.Errorf(codes.Internal, "files: trash %s: %v", path, err)
		}
	}
	if id, ok, err := s.row(dir, path); err != nil {
		return nil, err
	} else if ok {
		if err := s.mem.Retire(id); err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("files: %s trashed but its row not retired: %w", path, err)
		}
	}
	s.emitGridChanged(dir)
	return &pb.DeleteTileResponse{}, nil
}
