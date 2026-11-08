package files

// A directory as a grid: its listing joined with the rows the user made
// (store.Namespace.Overlay), every listing a verdict on every name in it.

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// listed is one name a directory holds: what the store keeps of it, and the
// facts read off disk on every read.
type listed struct {
	store.Entry
	target       string // a symlink to a file: where it lands, "" outside every root
	stamp        string // a text file's content stamp
	readOnly     string
	presentation string
	preview      int64 // an image's preview stamp
}

// entryOf is what dir lists for e: the one owner, so a listing and a watched
// file's change carry the same facts.
func (s *Source) entryOf(e diskEntry) listed {
	out := listed{Entry: store.Entry{Key: e.path, Label: e.name}}
	switch e.kind {
	case diskDir:
		out.Kind, out.Child = rpc.KindDoor, e.path
	case diskLink:
		s.linkFacts(&out, e)
	default:
		if !fileFacts(&out, e.path) {
			out.stamp = contentStamp(e.mtime, e.size)
			out.readOnly = readOnly(e.path, e.size)
		}
	}
	return out
}

// fileFacts is a file's kind of content, read from its name, and whether it
// is a page file: a url tile whose page the node serves, so it has no
// address of its own. Any other file is a text tile.
func fileFacts(out *listed, path string) (page bool) {
	if isPage(path) {
		out.Kind, out.ServesPage = rpc.KindURL, true
		out.preview = previewStamp(path)
		return true
	}
	out.Kind, out.presentation = rpc.KindText, presentation(filepath.Base(path))
	return false
}

// linkFacts lists a symlink as a link to where it lands: to a directory as a
// door onto that directory's own grid, so a link back up the tree is one grid
// and no endless descent, and to a file as a link tile naming the file's path.
// One that lands nowhere, or outside every root, names where it points, which
// reads dead.
func (s *Source) linkFacts(out *listed, e diskEntry) {
	at, held := s.spell(e.target)
	switch {
	case held && e.targetKind == diskDir:
		out.Kind, out.Child = rpc.KindDoor, at
		return
	case held && e.targetKind == diskFile:
		fileFacts(out, at)
		out.target = at
	default:
		fileFacts(out, e.path)
		at = e.target
	}
	out.preview, out.LinkTarget = 0, idOf(at)
}

// listing is one directory as GetGrid answers it; rows[i] is tiles[i].
type listing struct {
	dir     string
	since   uint64 // the directory's servedDir.gen before the disk was read
	gid     int64  // the directory's grid row, 0 when nobody touched it
	dark    bool
	entries []listed
	rows    []store.ExtTile
	tiles   []*pb.Tile
	grid    *pb.Grid
}

// synthesize reads dir and joins it with its rows. A directory that is gone
// is dead, a root that is gone is dark, and one that will not read is dark:
// its rows answer as they stand and nothing retires. Where its links land
// is noted after the read, since noting may list those directories too.
func (s *Source) synthesize(dir string) (*listing, error) {
	l, err := s.read(dir)
	if err == nil && !l.dark {
		s.noteLinks(dir, l.entries)
	}
	return l, err
}

func (s *Source) read(dir string) (*listing, error) {
	s.moving.RLock()
	defer s.moving.RUnlock()
	gid, _, err := s.mem.LookupGrid(dir)
	if err != nil {
		return nil, err
	}
	l := &listing{dir: dir, gid: gid, since: s.servedGen(dir)}
	ents, err := readDir(dir)
	switch {
	case err == nil:
		s.noteRead(dir, "")
	case gone(err) && !s.isRoot(dir):
		s.noteRead(dir, "")
		return nil, gwerr.DeadLink("", "files: %s is gone", dir)
	default:
		why := fmt.Sprintf("%s cannot be read: %v", dir, pathErr(err))
		if gone(err) {
			why = fmt.Sprintf("root %s does not exist", dir)
		}
		s.noteRead(dir, why)
		l.dark = true
	}
	present := map[string]bool{}
	for _, e := range ents {
		le := s.entryOf(e)
		l.entries = append(l.entries, le)
		present[le.Key] = true
	}
	if !l.dark && gid != 0 {
		if err := s.mem.Sweep(gid, present); err != nil {
			return nil, err
		}
		if err := s.mem.Refresh(gid, storeEntries(l.entries)); err != nil {
			return nil, err
		}
	}
	if l.rows, err = s.mem.Overlay(gid, storeEntries(l.entries)); err != nil {
		return nil, err
	}
	sel, err := s.selection(gid)
	if err != nil {
		return nil, err
	}
	l.grid = &pb.Grid{Id: idOf(dir), AcceptsTiles: proto.Bool(false), Writable: true, Moves: proto.Bool(true),
		Glyph: rpc.GlyphFolder, Selection: sel}
	if l.tiles, err = s.buildTiles(dir, l.rows, l.entries); err != nil {
		return nil, err
	}
	return l, nil
}

func storeEntries(es []listed) []store.Entry {
	out := make([]store.Entry, len(es))
	for i, e := range es {
		out[i] = e.Entry
	}
	return out
}

// buildTiles names each row by its path and adds what the disk says of it.
// A door is a link when the grid it opens is not its own path's, a symlink's.
func (s *Source) buildTiles(dir string, rows []store.ExtTile, entries []listed) ([]*pb.Tile, error) {
	byKey := map[string]listed{}
	for _, e := range entries {
		byKey[e.Key] = e
	}
	out := make([]*pb.Tile, 0, len(rows))
	for _, r := range rows {
		t := proto.CloneOf(r.Tile)
		t.Id, t.GridId, t.ChildGridId = idOf(r.Key), idOf(dir), ""
		e, ok := byKey[r.Key]
		child := e.Child
		if !ok && r.ChildGridID != 0 {
			var err error
			if child, err = s.mem.GridKey(r.ChildGridID); err != nil {
				return nil, err
			}
		}
		if child != "" {
			t.ChildGridId, t.Link = idOf(child), child != r.Key
		}
		if ok && t.LinkTargetId == "" {
			t.ContentStamp, t.ReadOnly, t.ServesPage = e.stamp, e.readOnly, e.ServesPage
			t.TextPresentation = e.presentation
			t.PreviewBlobId = previewKey(t.PreviewBlobId, e.preview)
		}
		out = append(out, t)
	}
	return out, nil
}

// previewKey is a file tile's preview key: the row's screenshot once one
// exists, else the image's own picture, its stamp negated so the two number
// spaces never meet in the client's (tile, key) cache.
func previewKey(blobID, stamp int64) int64 {
	switch {
	case blobID != 0:
		return blobID
	case stamp > 0:
		return -stamp
	}
	return 0
}

// pagePreviewStale is a page file's screenshot of bytes that have since
// moved. The freeze gesture's screenshot is the user's and stays, and a link
// has no preview of its own.
func pagePreviewStale(e listed, row *pb.Tile) bool {
	return e.ServesPage && e.LinkTarget == "" && row.PreviewBlobId != 0 && !row.UrlFrozen
}

// at is the index of path's row, -1 when the listing has none.
func (l *listing) at(path string) int {
	return slices.IndexFunc(l.rows, func(r store.ExtTile) bool { return r.Key == path })
}

func (l *listing) tile(path string) *pb.Tile {
	if i := l.at(path); i >= 0 {
		return l.tiles[i]
	}
	return nil
}

func (l *listing) entry(path string) (listed, bool) {
	i := slices.IndexFunc(l.entries, func(e listed) bool { return e.Key == path })
	if i < 0 {
		return listed{}, false
	}
	return l.entries[i], true
}

func (s *Source) GetGrid(_ context.Context, req *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	dir, err := s.dirOf(req.GridId)
	if err != nil {
		return nil, err
	}
	l, err := s.synthesize(dir)
	if err != nil {
		return nil, err
	}
	return s.answer(l), nil
}

// answer is l as GetGrid answers it, noted as a listing a client holds, and
// announced when a change settled while it was read.
func (s *Source) answer(l *listing) *pb.GetGridResponse {
	if s.settle(l, false) {
		s.announce([]string{l.dir})
	}
	return &pb.GetGridResponse{Grid: l.grid, Tiles: l.tiles}
}

// tilePath reads a tile id as its path and the directory listing it. A root
// inside another root is a tile of the directory holding it; one no root
// holds is listed by no directory.
func (s *Source) tilePath(tileID string) (path, dir string, err error) {
	path, err = pathOf(tileID)
	if err != nil {
		return "", "", err
	}
	if err := s.inTree(path); err != nil {
		return "", "", s.refuse(path, err)
	}
	dir = filepath.Dir(path)
	if dir == path || s.inTree(dir) != nil {
		return "", "", gwerr.DeadLink("", "files: %s is a root, which no directory lists", path)
	}
	return path, dir, nil
}

func (s *Source) GetTile(_ context.Context, req *pb.GetTileRequest) (*pb.TileResponse, error) {
	t, err := s.tileByPath(req.TileId)
	if err != nil {
		return nil, err
	}
	return &pb.TileResponse{Tile: t}, nil
}

func (s *Source) tileByPath(tileID string) (*pb.Tile, error) {
	path, dir, err := s.tilePath(tileID)
	if err != nil {
		return nil, err
	}
	l, err := s.synthesize(dir)
	if err != nil {
		return nil, err
	}
	if t := l.tile(path); t != nil {
		return t, nil
	}
	return nil, l.absent(dir, path)
}

// absent is why path is not in its directory's listing l: the directory
// cannot be read, which says nothing of the file, or the file is gone.
func (l listing) absent(dir, path string) error {
	if l.dark {
		return status.Errorf(codes.Unavailable, "files: %s cannot be read and holds no row for %s", dir, path)
	}
	return gwerr.DeadLink("", "files: %s is gone", path)
}

// mint is the one place a file's row appears, at the placement GetGrid
// already answers, so touching a tile never moves it.
func (s *Source) mint(tileID string) (int64, error) {
	path, dir, err := s.tilePath(tileID)
	if err != nil {
		return 0, err
	}
	if id, ok, err := s.row(dir, path); err != nil || ok {
		return id, err
	}
	l, err := s.synthesize(dir)
	if err != nil {
		return 0, err
	}
	i, e, ok := l.at(path), listed{}, false
	if i >= 0 {
		e, ok = l.entry(path)
	}
	if !ok {
		return 0, l.absent(dir, path)
	}
	gid, err := s.mem.GridOf(dir)
	if err != nil {
		return 0, err
	}
	var child int64
	if e.Child != "" {
		if child, err = s.mem.GridOf(e.Child); err != nil {
			return 0, err
		}
	}
	r := l.rows[i]
	return s.mem.Mint(gid, e.Entry, child, r.X, r.Y, r.W, r.H)
}

// row is path's live row in dir, ok false when it has none.
func (s *Source) row(dir, path string) (int64, bool, error) {
	gid, ok, err := s.mem.LookupGrid(dir)
	if err != nil || !ok {
		return 0, false, err
	}
	return s.mem.LiveTileID(gid, path)
}

// selection is a directory's selection, each row named by its path.
func (s *Source) selection(gid int64) ([]string, error) {
	if gid == 0 {
		return nil, nil
	}
	rows, err := s.mem.Selection(gid)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, id := range rows {
		_, key, _, err := s.mem.TileKey(id)
		if err != nil {
			return nil, err
		}
		out = append(out, idOf(key))
	}
	return out, nil
}

// listingSum is a listing as a client holds it, without the content stamps:
// bytes that moved are their file's TileChanged, and a directory whose files
// are written while its names stay has not moved.
type listingSum [sha256.Size]byte

func sumOf(l *listing) listingSum {
	if l.dark {
		return listingSum{}
	}
	h := sha256.New()
	for _, e := range l.entries {
		fmt.Fprintf(h, "%q %q %q %t %q %q %q %q %d\n", e.Key, e.Label, e.Kind, e.ServesPage, e.LinkTarget,
			e.Child, e.readOnly, e.presentation, e.preview)
	}
	var out listingSum
	h.Sum(out[:0])
	return out
}

// servedDir is the listings a client may hold of one directory since it was
// last announced, and the last listing noted, with gen counting notes so a
// read can tell one landed while it was in flight.
type servedDir struct {
	sums []listingSum
	last listingSum
	gen  uint64
}

func (s *Source) servedGen(dir string) uint64 {
	s.servedMu.Lock()
	defer s.servedMu.Unlock()
	if g := s.served[dir]; g != nil {
		return g.gen
	}
	return 0
}

// settle notes listing l and reports whether its directory must be
// announced, in one critical section so no read lands between check and
// note: a read moved if another listing was noted while it was in flight,
// and a check also if it differs from every listing a client may hold. A
// directory nobody read is noted, not announced.
func (s *Source) settle(l *listing, check bool) (moved bool) {
	sum := sumOf(l)
	s.servedMu.Lock()
	defer s.servedMu.Unlock()
	if s.served == nil {
		s.served = map[string]*servedDir{}
	}
	g := s.served[l.dir]
	if g == nil {
		g = &servedDir{}
		s.served[l.dir] = g
	}
	if check && l.dark && len(g.sums) == 0 {
		return false
	}
	raced := g.gen != l.since && g.last != sum
	differs := check && len(g.sums) > 0 && !slices.Equal(g.sums, []listingSum{sum})
	switch {
	case raced || differs:
		g.sums, moved = []listingSum{sum}, true
	case !slices.Contains(g.sums, sum):
		g.sums = append(g.sums, sum)
	}
	g.last = sum
	g.gen++
	return moved
}

// errGone is true when err is the answer for a path that is not there.
func errGone(err error) bool { return gwerr.IsDeadLink(err) || errors.Is(err, store.ErrNotFound) }
