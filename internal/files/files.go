// Package files is the node's files: every directory on the host, shown as
// grids, as one namespace.Namespace, starting from the roots server.yaml's
// files: declares. A file or directory
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
	"hash/fnv"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

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
	trash func(dir *os.File, name string) error
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

	// writing serializes the saves of each file, by where its bytes live: a
	// save holds its file's stripe from its claim to its rename.
	writing [64]sync.Mutex
}

func (s *Source) lockPath(real string) (unlock func()) {
	h := fnv.New32a()
	h.Write([]byte(real))
	m := &s.writing[h.Sum32()%uint32(len(s.writing))]
	m.Lock()
	return m.Unlock
}

var _ namespace.Namespace = (*Source)(nil)

// New is the files starting from roots, their rows in mem. trash moves the
// name in an opened directory to the trash; nil is the OS trash. Close ends
// the watch. A relative root is refused, since a path id names an absolute
// path and would name none of its files.
func New(mem *store.FileRows, roots []Root, trash func(dir *os.File, name string) error) (*Source, error) {
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

var errIrregular = errors.New("is not a regular file")

func idOf(path string) string { return rpc.KeyTileID(path) }

// pathOf reads an id as the absolute path it names, dead when it names none.
func pathOf(id string) (string, error) {
	p, ok := rpc.TileKey(id)
	if !ok || !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsRune(p, 0) {
		return "", gwerr.DeadLink("", "files: %q names no path", id)
	}
	return p, nil
}

func (s *Source) isRoot(path string) bool {
	return slices.ContainsFunc(s.roots, func(r Root) bool { return r.Path == path })
}

// spell is target, a path with every link followed, as the first root holding
// it spells it, so a link into a root opens the grid the root lists.
func (s *Source) spell(target string) string {
	for _, r := range s.roots {
		real, err := filepath.EvalSymlinks(r.Path)
		if err != nil {
			real = r.Path
		}
		if underRoot(real, target) {
			rel, _ := filepath.Rel(real, target)
			return filepath.Join(r.Path, rel)
		}
	}
	return target
}

// opened is a file's bytes, opened where they live with every link followed,
// which is its name.
type opened struct {
	*os.File
	fi os.FileInfo
}

// open opens path's bytes, every link followed, when they are a regular file:
// by its mode before the open and again by fstat after it (errIrregular),
// nonblocking so that a FIFO swapped in between answers its mode rather than
// waiting for a writer. A test counts its calls.
var open = func(path string) (*opened, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errIrregular
	}
	f, err := os.OpenFile(real, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if fi, err = f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		if err == nil {
			err = errIrregular
		}
		return nil, err
	}
	return &opened{File: f, fi: fi}, nil
}

// openDir opens dir, for a rename by name in it.
func openDir(dir string) (*os.File, error) {
	resolved()
	return os.Open(dir)
}

// resolved runs before a directory is opened to take a name: a test's seam.
var resolved = func() {}

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

// setHealth applies one edit and announces a transition, in its order.
func (s *Source) setHealth(edit func()) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	was := s.healthLocked()
	edit()
	now := s.healthLocked()
	if now == was {
		return
	}
	trace.Emit("files", "health", fmt.Sprintf("dark %t, told %t", now.dark, now.liveOff == ""),
		map[string]string{"dark": strconv.FormatBool(now.dark), "detail": now.detail, "liveOff": now.liveOff})
	s.hub.Publish(now.event())
}

// noteRead records whether dir read, why "" when it did. Only a directory
// the view holds darkens the files: a read of one nobody shows, an agent's
// or a find's, says nothing of their health.
func (s *Source) noteRead(dir, why string) {
	s.scopeMu.Lock()
	scoped := slices.Contains(s.scopeLocked(), dir)
	s.scopeMu.Unlock()
	if why != "" && !scoped {
		return
	}
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
