// Package files is the node's files: the directories server.yaml's files:
// declares, shown as grids, as one namespace.Namespace. A file or directory
// is named by its absolute path (rpc.KeyTileID), a directory's grid and the
// door showing it alike; the node's facts about one (placement, framing, a
// typed name, a frozen preview, the selection) are store rows minted on
// touch (store.FileRows), and every other fact is read off disk on every
// read. Ids handed in and out are bare; the router qualifies them. A path is
// canonical only as spelled: two spellings of one file, through a symlinked
// root and by its real path, or two cases on a case-insensitive disk, are
// two ids.
package files

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/eventhub"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/trace"
)

// Root is one directory files: declares: an absolute path and its label.
type Root struct {
	Path, Label string
}

// Source is the files as one namespace.Namespace.
type Source struct {
	roots []Root
	mem   *store.FileRows
	trash func(path string) error
	hub   *eventhub.Hub[*pb.Event]
	watch *watcher

	// dark is, per directory, why its last read failed, until it leaves the
	// view (rescope); liveOff is why the watch is not told. setHealth is
	// their one writer.
	healthMu sync.Mutex
	dark     map[string]string
	liveOff  string

	// shown is the directories some session shows, and links, per directory,
	// the directories its symlinks to files land in; the watch follows both.
	scopeMu sync.Mutex
	shown   []string
	links   map[string][]string

	// served is, per directory, what a client may hold of it (settle).
	servedMu sync.Mutex
	served   map[string]*servedDir

	// told is, per file, the content stamp it was last told under (fresh),
	// while the view holds its directory (rescope).
	toldMu sync.Mutex
	told   map[string]string

	// moving keeps a listing from retiring the row a move is rekeying: the
	// old directory lists the path no more, and the new one does not hold
	// the row yet.
	moving sync.RWMutex
}

var _ namespace.Namespace = (*Source)(nil)

// New is the files over roots, their rows in mem. trash moves a deleted path
// out of the tree; nil is the OS trash. Close ends the watch. A relative root
// is refused, since a path id names an absolute path and would name none of
// its files.
func New(mem *store.FileRows, roots []Root, trash func(path string) error) (*Source, error) {
	if trash == nil {
		trash = osTrash
	}
	out := make([]Root, len(roots))
	for i, r := range roots {
		if !filepath.IsAbs(r.Path) {
			return nil, fmt.Errorf("files: root %q is not an absolute path", r.Path)
		}
		p := filepath.Clean(r.Path)
		out[i] = Root{Path: p, Label: r.Label}
		if out[i].Label == "" {
			out[i].Label = filepath.Base(p)
		}
	}
	s := &Source{roots: out, mem: mem, trash: trash, hub: eventhub.New(rpc.EventKey), dark: map[string]string{}}
	s.watch = newWatcher(s)
	return s, nil
}

// Close stops watching the disk.
func (s *Source) Close() { s.watch.close() }

// errOutside is a path no root holds: outside every root, or under one
// through a directory that is a symlink, which is listed as a link and never
// entered.
var errOutside = errors.New("is outside every root")

func idOf(path string) string { return rpc.KeyTileID(path) }

// pathOf reads an id as the absolute path it names, dead when it names none.
func pathOf(id string) (string, error) {
	p, ok := rpc.TileKey(id)
	if !ok || !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsRune(p, 0) {
		return "", gwerr.DeadLink("", "files: %q names no path", id)
	}
	return p, nil
}

// rootOf is the innermost root holding path.
func (s *Source) rootOf(path string) (Root, bool) {
	var best Root
	found := false
	for _, r := range s.roots {
		if underRoot(r.Path, path) && (!found || len(r.Path) > len(best.Path)) {
			best, found = r, true
		}
	}
	return best, found
}

func (s *Source) isRoot(path string) bool {
	return slices.ContainsFunc(s.roots, func(r Root) bool { return r.Path == path })
}

// inTree is nil when path is under a root through real directories only. A
// path whose way down is gone is in the tree, and absent there.
func (s *Source) inTree(path string) error {
	r, ok := s.rootOf(path)
	if !ok {
		return errOutside
	}
	rel, _ := filepath.Rel(r.Path, filepath.Dir(path))
	if rel == "." || path == r.Path {
		return nil
	}
	at := r.Path
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		at = filepath.Join(at, part)
		fi, err := os.Lstat(at)
		switch {
		case gone(err):
			return nil
		case err != nil:
			return err
		case fi.Mode()&os.ModeSymlink != 0:
			return errOutside
		}
	}
	return nil
}

// dirOf reads a grid id as its directory: in the tree and, unless it is a
// root, not itself a link, which opens its target's grid instead.
func (s *Source) dirOf(gridID string) (string, error) {
	dir, err := pathOf(gridID)
	if err != nil {
		return "", err
	}
	if err := s.inTree(dir); err != nil {
		return "", s.refuse(dir, err)
	}
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 && !s.isRoot(dir) {
		return "", s.refuse(dir, errOutside)
	}
	return dir, nil
}

// refuse answers a path the tree does not hold, or cannot read right now.
func (s *Source) refuse(path string, err error) error {
	if errors.Is(err, errOutside) {
		return gwerr.DeadLink("", "files: %s %v", path, err)
	}
	return status.Errorf(codes.Unavailable, "files: read %s: %v", path, pathErr(err))
}

// spell is where target, a path with every link followed, sits in the tree
// as a root spells it, ok false when no root holds it.
func (s *Source) spell(target string) (string, bool) {
	for _, r := range s.roots {
		real, err := filepath.EvalSymlinks(r.Path)
		if err != nil {
			real = r.Path
		}
		if !underRoot(real, target) {
			continue
		}
		rel, _ := filepath.Rel(real, target)
		p := filepath.Join(r.Path, rel)
		if s.inTree(p) == nil {
			return p, true
		}
	}
	return "", false
}

// content is where path's bytes live, every link followed, errOutside when
// that leaves every root.
func (s *Source) content(path string) (string, error) {
	if err := s.inTree(path); err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	for _, r := range s.roots {
		rr, err := filepath.EvalSymlinks(r.Path)
		if err == nil && underRoot(rr, real) {
			return real, nil
		}
	}
	return "", errOutside
}

// Info is the files' row of the roster: one grid per root, each with its
// framing.
func (s *Source) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	resp := &pb.InfoResponse{DisplayName: "files", Glyph: rpc.GlyphFolder}
	for _, r := range s.roots {
		g := &pb.SourceGrid{Id: r.Path, Label: r.Label, Glyph: rpc.GlyphFolder, GridId: idOf(r.Path)}
		gid, ok, err := s.mem.LookupGrid(r.Path)
		if err != nil {
			return nil, err
		}
		if ok {
			v, err := s.mem.RootFraming(gid)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			g.ViewCx, g.ViewCy, g.ViewZoom = v.Wire()
		}
		resp.SourceGrids = append(resp.SourceGrids, g)
	}
	return resp, nil
}

func (s *Source) Handshake(context.Context, *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "files: the node's handshake lists the files; they have none of their own")
}

func (s *Source) CreateTile(context.Context, *pb.CreateTileRequest) (*pb.TileResponse, error) {
	return nil, status.Error(codes.Unimplemented, "files: a directory takes no new tiles; a file is made on disk")
}

func (s *Source) CopyTile(context.Context, *pb.CopyTileRequest) (*pb.TileResponse, error) {
	return nil, status.Error(codes.Unimplemented, "files: a directory takes no new tiles, so a copy into one is refused")
}

func (s *Source) ShellTmuxAlive(context.Context, *pb.ShellTmuxAliveRequest) (*pb.ShellTmuxAliveResponse, error) {
	return nil, status.Error(codes.Unimplemented, "files: a file is no shell; a tmux is home's")
}

func (s *Source) OpenShell(context.Context, func() (*pb.OpenShellRequest, error), func(*pb.OpenShellResponse) error) error {
	return status.Error(codes.Unimplemented, "files: a file is no shell; a tmux is home's")
}

// Subscribe serves the files' events: GridChanged, TileChanged,
// GridFramingChanged, SelectionChanged and the health event. A subscriber
// arriving while a directory is dark or the watch is not told is told at
// once.
func (s *Source) Subscribe(ctx context.Context, _ *pb.SubscribeRequest, send func(*pb.Event) error) error {
	ch, detach := s.hub.Subscribe()
	defer detach()
	if err := send(nil); err != nil {
		return err
	}
	if h := s.health(); h != (health{}) {
		if err := send(h.event()); err != nil {
			return err
		}
	}
	return namespace.Relay(ctx, ch, send)
}

// health is the files as one health event tells them, naming them by their
// own segment, the key form of the empty path, which the router qualifies with
// the node like every id they answer (rpc.FilesOwner); the zero value is
// every directory read and the watch told.
type health struct {
	dark            bool
	detail, liveOff string
}

func (h health) event() *pb.Event {
	ev := rpc.HealthEvent(rpc.KeyTileID(""), !h.dark, h.detail)
	ev.GetSourceHealth().LiveUpdatesOff = h.liveOff
	ev.GetSourceHealth().Told = proto.Bool(h.liveOff == "")
	return ev
}

func (s *Source) health() health {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	return s.healthLocked()
}

// healthLocked names the first dark directory by path, so the detail does
// not flap between two.
func (s *Source) healthLocked() health {
	h := health{liveOff: s.liveOff}
	if len(s.dark) > 0 {
		dirs := make([]string, 0, len(s.dark))
		for d := range s.dark {
			dirs = append(dirs, d)
		}
		slices.Sort(dirs)
		h.dark, h.detail = true, s.dark[dirs[0]]
	}
	return h
}

// setHealth applies one edit and announces a transition.
func (s *Source) setHealth(edit func()) {
	s.healthMu.Lock()
	was := s.healthLocked()
	edit()
	now := s.healthLocked()
	s.healthMu.Unlock()
	if now == was {
		return
	}
	trace.Emit("files", "health", fmt.Sprintf("dark %t, told %t", now.dark, now.liveOff == ""),
		map[string]string{"dark": strconv.FormatBool(now.dark), "detail": now.detail, "liveOff": now.liveOff})
	s.hub.Publish(now.event())
}

// noteRead records whether dir read, why "" when it did.
func (s *Source) noteRead(dir, why string) {
	s.setHealth(func() {
		if why == "" {
			delete(s.dark, dir)
		} else {
			s.dark[dir] = why
		}
	})
}

func (s *Source) noteWatch(why string) {
	s.setHealth(func() { s.liveOff = why })
}

// emitGridChanged announces a directory a write moved; what a client held of
// it is forgotten.
func (s *Source) emitGridChanged(dir string) {
	s.servedMu.Lock()
	delete(s.served, dir)
	s.servedMu.Unlock()
	s.publishGridChanged(dir)
}

func (s *Source) publishGridChanged(dir string) {
	s.hub.Publish(&pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: idOf(dir)}}})
}
