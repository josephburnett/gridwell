package files

// The writes the node keeps on a file's row, and the two the disk takes: a
// move renames the file, a delete trashes it.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/doctype"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// MoveTile writes a placement on the file's row, minting it. A move to
// another directory renames the file, and its row follows under the new
// path's id, the old one retired; a move to another source is refused.
func (s *Source) MoveTile(_ context.Context, req *pb.MoveTileRequest) (*pb.TileResponse, error) {
	path, dir, err := s.tilePath(req.TileId)
	if err != nil {
		return nil, err
	}
	to := dir
	if req.GridId != "" {
		if rpc.ShapeOf(req.GridId) != rpc.ShapeKey {
			return nil, status.Error(codes.InvalidArgument, "files: "+rpc.MoveAcross)
		}
		if to, err = s.dirOf(req.GridId); err != nil {
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

// CreateTile makes a text tile a new markdown file in the directory holding
// its body, named for the tile or else the body's first line (fileName), its
// row minted at the placement asked; a directory takes nothing else
// (rpc.IntoDirectory). A name already there is refused and nothing written.
func (s *Source) CreateTile(_ context.Context, req *pb.CreateTileRequest) (*pb.TileResponse, error) {
	t := req.GetTile()
	if why := rpc.IntoDirectory(t); why != "" {
		return nil, status.Error(codes.InvalidArgument, "files: "+why)
	}
	dir, err := s.dirOf(req.GridId)
	if err != nil {
		return nil, err
	}
	name := fileName(cmp.Or(t.AltText, doctype.AltFromSource(string(req.Content))))
	path := filepath.Join(dir, name)
	if len(req.Content) > bodyCap {
		return nil, status.Errorf(codes.InvalidArgument, "files: %s: a body is at most %d MiB", path, bodyCap>>20)
	}
	if err := s.create(dir, name, req.Content); err != nil {
		return nil, err
	}
	l, err := s.synthesize(dir)
	if err != nil {
		return nil, err
	}
	e, ok := l.entry(path)
	if !ok {
		return nil, l.absent(dir, path)
	}
	gid, err := s.mem.GridOf(dir)
	if err == nil {
		_, err = s.mem.Mint(gid, e.Entry, 0, t.X, t.Y, t.W, t.H)
	}
	return s.changedTile(path, true, err, true)
}

// create writes data as the new file name in dir, made exclusively, so a
// name taken meanwhile is refused and never overwritten; a write that fails
// takes its file away again.
func (s *Source) create(dir, name string, data []byte) error {
	rr, rel, err := s.content(dir)
	var root *os.Root
	if err == nil {
		resolved()
		root, err = os.OpenRoot(rr)
	}
	var f *os.File
	if err == nil {
		defer root.Close()
		f, err = root.OpenFile(filepath.Join(rel, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	}
	if errors.Is(err, iofs.ErrExist) {
		return status.Errorf(codes.InvalidArgument, "files: %q is already in %s, and a copy replaces nothing", name, dir)
	}
	if err == nil {
		if _, err = f.Write(data); err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = root.Remove(filepath.Join(rel, name))
		}
	}
	if err != nil {
		return diskRefusal("written", filepath.Join(dir, name), err)
	}
	return nil
}

// fileName is the file a text tile made in a directory is named, from the
// tile's name: no separator or control character (NUL among them), no
// leading dot, trimmed, at most 120 bytes cut on a rune (below every
// filesystem's 255), "untitled" when nothing is left, and ending in .md.
func fileName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimLeft(strings.TrimSpace(name), ". ")
	for len(name) > 120 {
		_, n := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-n]
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "untitled"
	}
	if !rpc.Markdown(name) {
		name += ".md"
	}
	return name
}

// rename moves path into the directory to under its name and answers where
// it landed. It replaces nothing. Each refusal is a verdict with its reason,
// never the conflict a stale claim is, so the client snaps the move back
// rather than reconciling; a disk that cannot answer is the one outage.
func (s *Source) rename(path, to string) (string, error) {
	refuse := func(c codes.Code, why string, a ...any) error {
		return status.Errorf(c, "files: %s cannot move to %s: "+why, append([]any{path, to}, a...)...)
	}
	into, err := s.openDir(to)
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
	from, err := s.openDir(filepath.Dir(path))
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
	dir, err := s.dirOf(req.GridId)
	if err != nil {
		return nil, err
	}
	var add, remove []int64
	for _, id := range req.Add {
		if _, in, err := s.tilePath(id); err != nil || in != dir {
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
		path, in, err := s.tilePath(id)
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
	case gone(err), errors.Is(err, errOutside), gwerr.IsDeadLink(err):
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
		d, err := s.openDir(dir)
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
	s.emitGridChanged(dir)
	return &pb.DeleteTileResponse{}, nil
}
