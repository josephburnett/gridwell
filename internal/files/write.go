package files

// The writes the node keeps on a file's row, and the four the disk takes: a
// create makes a file or a directory, a copy copies a file, a move renames
// it, a delete trashes it.

import (
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// MoveTile writes a placement on the file's row, minting it. A move to
// another directory renames the file, and its row follows under the new
// path's id, the old one retired; a move to another source is refused.
func (s *Source) MoveTile(_ context.Context, req *pb.MoveTileRequest) (*pb.TileResponse, error) {
	path, dir, err := tilePath(req.TileId)
	if err != nil {
		return nil, err
	}
	to := dir
	if req.GridId != "" {
		if rpc.ShapeOf(req.GridId) != rpc.ShapeKey {
			return nil, status.Error(codes.InvalidArgument, "files: "+rpc.MoveFileOver)
		}
		if to, err = pathOf(req.GridId); err != nil {
			return nil, err
		}
	}
	if to != dir && s.isRoot(path) {
		return nil, status.Errorf(codes.InvalidArgument, "files: %s is a root, which is config and is not renamed", path)
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

// moveAcross renames path into to, and the overlay under path follows it
// (store.FileRows.Rekey). The claim, and a gone directory's Gridwell tiles
// where it would land, are checked before the rename, since a rename is not
// undone.
func (s *Source) moveAcross(req *pb.MoveTileRequest, path, dir, to string) (*pb.TileResponse, error) {
	t, err := s.tileByPath(req.TileId)
	if err != nil {
		return nil, err
	}
	if err := store.ClaimPlacement(t, req.Claim, false); err != nil {
		return nil, gwerr.ToStatus(err)
	}
	if held, err := s.mem.HeldAt(filepath.Join(to, filepath.Base(path))); err != nil || held {
		if err == nil {
			err = status.Errorf(codes.InvalidArgument,
				"files: %s cannot move to %s: a gone directory's Gridwell tiles are there; move them out first", path, to)
		}
		return nil, err
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
		if err == nil {
			err = s.mem.Rekey(path, landed)
		}
		return landed, changed, err
	}()
	if err != nil {
		return nil, gwerr.ToStatus(err)
	}
	s.emitGridChanged(dir)
	return s.changedTile(landed, changed, nil, true)
}

// CreateTile makes the disk side's tile, CreateTileRequest.file: an empty
// file for any kind but a door, whose kind is then its name's, or a directory
// for a door, named tile.alt_text, its row minted at the placement asked. A
// name already there, one that names no single entry, or a directory that is
// not writable is refused with the reason, and nothing is written. A Gridwell
// tile on a directory is home's row, never asked here.
func (s *Source) CreateTile(_ context.Context, req *pb.CreateTileRequest) (*pb.TileResponse, error) {
	t := req.GetTile()
	if !req.File {
		return nil, status.Error(codes.InvalidArgument, "files: a Gridwell tile on a directory is home's row")
	}
	if len(req.Content) > 0 || rpc.LeafLink(t) || t.GetLink() || t.GetChildGridId() != "" {
		return nil, status.Error(codes.InvalidArgument, "files: a new file or directory is born empty and is no link")
	}
	dir, err := pathOf(req.GridId)
	if err != nil {
		return nil, err
	}
	name := t.GetAltText()
	if why := entryName(name); why != "" {
		return nil, status.Errorf(codes.InvalidArgument, "files: %q %s", name, why)
	}
	path := filepath.Join(dir, name)
	if err := create(dir, name, rpc.IsDoorKind(t.GetKind())); err != nil {
		return nil, err
	}
	return s.minted(dir, path, t)
}

// minted reads path back from its directory's listing and mints its row at
// t's placement, the one way a write that made an entry lands it.
func (s *Source) minted(dir, path string, t *pb.Tile) (*pb.TileResponse, error) {
	l, err := s.synthesize(dir)
	if err != nil {
		return nil, err
	}
	e, ok := l.entry(path)
	if !ok {
		return nil, l.absent(dir, path)
	}
	gid, err := s.mem.GridOf(dir)
	var child int64
	if err == nil && e.Child != "" {
		child, err = s.mem.GridOf(e.Child)
	}
	if err == nil {
		_, err = s.mem.Mint(gid, e.Entry, child, t.X, t.Y, max(t.W, 1), max(t.H, 1))
	}
	return s.changedTile(path, true, err, true)
}

// entryName is why name cannot be one new entry of a directory, "" when it
// can: it is empty, a dot or two, or holds a separator or a NUL.
func entryName(name string) string {
	switch {
	case name == "", name == ".", name == "..":
		return "names no entry"
	case strings.ContainsAny(name, "/\\\x00"):
		return "holds a separator or a NUL, and names no single entry"
	}
	return ""
}

// CopyTile copies a file on disk into a directory, under its own name, made
// exclusively so nothing is replaced, its row minted at the cell asked. It
// is rpc.CopyFile's executor, the router asking it of a file alone.
func (s *Source) CopyTile(_ context.Context, req *pb.CopyTileRequest) (*pb.TileResponse, error) {
	from, _, err := tilePath(req.TileId)
	if err != nil {
		return nil, err
	}
	dir, err := pathOf(req.DestGridId)
	if err != nil {
		return nil, err
	}
	src, err := open(from)
	if err != nil {
		return nil, diskRefusal("read", from, err)
	}
	defer src.Close()
	name := filepath.Base(from)
	path := filepath.Join(dir, name)
	if err := create(dir, name, false); err != nil {
		return nil, err
	}
	if err := fill(path, src); err != nil {
		return nil, err
	}
	return s.minted(dir, path, &pb.Tile{X: req.X, Y: req.Y, W: 1, H: 1})
}

// fill writes src's bytes into the empty file path create made, taking the
// file away again when the write fails, so a copy is whole or absent.
func fill(path string, src io.Reader) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err == nil {
		if _, err = io.Copy(f, src); err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		_ = os.Remove(path)
		return diskRefusal("written", path, err)
	}
	return nil
}

// create makes the new entry name in dir, a directory or an empty file made
// exclusively, so a name taken meanwhile is refused and never overwritten.
func create(dir, name string, isDir bool) error {
	resolved()
	path := filepath.Join(dir, name)
	var err error
	if isDir {
		err = os.Mkdir(path, 0o755)
	} else {
		var f *os.File
		if f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644); err == nil {
			err = f.Close()
		}
	}
	if errors.Is(err, iofs.ErrExist) {
		return status.Errorf(codes.InvalidArgument, "files: %q is already in %s, and nothing is replaced", name, dir)
	}
	if err != nil {
		return diskRefusal("written", path, err)
	}
	return nil
}

// rename moves path into the directory to under its name and answers where
// it landed. It replaces nothing. Each refusal is a verdict with its reason,
// never the conflict a stale claim is, so the client snaps the move back
// rather than reconciling; a disk that cannot answer is the one outage.
func (s *Source) rename(path, to string) (string, error) {
	refuse := func(c codes.Code, why string, a ...any) error {
		return status.Errorf(c, "files: %s cannot move to %s: "+why, append([]any{path, to}, a...)...)
	}
	into, err := openDir(to)
	var fi os.FileInfo
	if err == nil {
		defer into.Close()
		fi, err = into.Stat()
	}
	switch {
	case gone(err):
		return "", refuse(codes.InvalidArgument, "that directory is gone")
	case err != nil:
		return "", refuse(codes.Unavailable, "%v", pathErr(err))
	case !fi.IsDir():
		return "", refuse(codes.InvalidArgument, "that is not a directory")
	case underRoot(path, to):
		return "", refuse(codes.InvalidArgument, "a directory cannot move into itself")
	}
	if _, err := os.Lstat(path); err != nil {
		return "", refuse(codes.InvalidArgument, "it is gone")
	}
	name := filepath.Base(path)
	from, err := openDir(filepath.Dir(path))
	if err == nil {
		err = renameAt(from, name, into, name)
		from.Close()
	}
	switch {
	case errors.Is(err, iofs.ErrExist):
		return "", refuse(codes.InvalidArgument, "%q is already there", name)
	case errors.Is(err, iofs.ErrPermission), errors.Is(err, syscall.EROFS):
		return "", refuse(codes.PermissionDenied, "%v", pathErr(err))
	case err != nil:
		return "", refuse(codes.InvalidArgument, "%v", pathErr(err))
	}
	return filepath.Join(to, name), nil
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
	path, _, err := tilePath(req.TileId)
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
		dir, err := pathOf(req.RootGridId)
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
	path, _, err := tilePath(req.TileId)
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
// a grid's. An added file gets its row; an id naming no file in the
// directory, a row's among them, is ignored, added or removed.
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
	dir, err := pathOf(req.GridId)
	if err != nil {
		return nil, err
	}
	var add, remove []int64
	for _, id := range req.Add {
		if row, ok := gridwellRow(id); ok {
			add = append(add, row)
			continue
		}
		if _, in, err := tilePath(id); err != nil || in != dir {
			continue
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
		if row, ok := gridwellRow(id); ok {
			remove = append(remove, row)
			continue
		}
		path, in, err := tilePath(id)
		if err != nil {
			continue
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

// gridwellRow reads id as a Gridwell tile's row, one of home's placed on a
// directory, which a select names by its digits; the store ignores one not on
// the grid.
func gridwellRow(id string) (int64, bool) {
	if rpc.ShapeOf(id) != rpc.ShapeRow {
		return 0, false
	}
	row, err := strconv.ParseInt(id, 10, 64)
	return row, err == nil
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
		_, err = os.Lstat(path)
	}
	switch {
	case err == nil:
		presence = pb.ProbeResponse_PRESENCE_PRESENT
	case gone(err), gwerr.IsDeadLink(err):
		presence = pb.ProbeResponse_PRESENCE_GONE
	}
	return &pb.ProbeResponse{Presence: presence}, nil
}

// DeleteTile moves the file to the trash, a link itself and never what it
// points at, and retires its row once the path is gone. A path already gone
// is deleted. Every Gridwell tile over a directory deleted, or gone already,
// goes to home's trash (store.FileRows.TrashOver): two trashes, and
// restoring one does not restore the other.
func (s *Source) DeleteTile(_ context.Context, req *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	path, err := pathOf(req.TileId)
	if err != nil {
		return nil, err
	}
	if s.isRoot(path) {
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
	fi, err := os.Lstat(path)
	overlay := err == nil && fi.IsDir()
	switch {
	case gone(err):
		overlay = true
	case err != nil:
		return nil, status.Errorf(codes.Unavailable, "files: delete %s: %v", path, pathErr(err))
	default:
		d, err := openDir(dir)
		if err == nil {
			err = s.trash(d, filepath.Base(path))
			d.Close()
		}
		if err != nil {
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
	if overlay {
		if err := s.mem.TrashOver(path); err != nil {
			return nil, fmt.Errorf("files: %s trashed but its Gridwell tiles not: %w", path, err)
		}
	}
	s.emitGridChanged(dir)
	return &pb.DeleteTileResponse{}, nil
}
