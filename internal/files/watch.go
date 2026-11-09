package files

// The files are told of a change by the operating system, for the
// directories some session shows (SetView) and the directories their
// symlinks land in, so what watching costs is what is on screen. A name that
// came, went or moved is the GridChanged a write here would publish; a file
// written in place is the TileChanged carrying its bytes' news.

import (
	"context"
	"errors"
	iofs "io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// debounceWindow is how long a directory's changes gather before they are
// told, once each: a save or a build writes many files within milliseconds,
// and each tell costs a listing.
const debounceWindow = 200 * time.Millisecond

// SetView takes the files' share of the node's subscription as the
// directories shown. A directory the OS will not watch leaves live updates
// off, told false, until a view it takes; each directory the view adds is
// listed again, since a change between a client's read and the watch is
// otherwise told by nothing.
func (s *Source) SetView(_ context.Context, req *pb.SetViewRequest) (*pb.SetViewResponse, error) {
	var shown []string
	for _, id := range req.GetShownGridIds() {
		if dir, err := pathOf(id); err == nil {
			shown = append(shown, dir)
		}
	}
	s.scopeMu.Lock()
	s.shown = shown
	maps.DeleteFunc(s.links, func(d string, _ []string) bool { return !slices.Contains(shown, d) })
	scope := s.scopeLocked()
	s.scopeMu.Unlock()
	s.check(s.rescope(scope))
	return &pb.SetViewResponse{}, nil
}

// noteLinks records the directories dir's symlinks to files land in: a shown
// directory's links read their targets, so a change there is a change to
// what it shows, and one the watch takes in is listed, as a view's is.
func (s *Source) noteLinks(dir string, entries []listed) {
	var into []string
	for _, e := range entries {
		if e.target != "" && filepath.Dir(e.target) != dir {
			into = append(into, filepath.Dir(e.target))
		}
	}
	slices.Sort(into)
	into = slices.Compact(into)
	s.scopeMu.Lock()
	if slices.Equal(into, s.links[dir]) {
		s.scopeMu.Unlock()
		return
	}
	if s.links == nil {
		s.links = map[string][]string{}
	}
	if len(into) == 0 {
		delete(s.links, dir)
	} else {
		s.links[dir] = into
	}
	shown := slices.Contains(s.shown, dir)
	scope := s.scopeLocked()
	s.scopeMu.Unlock()
	if shown {
		s.check(s.rescope(scope))
	}
}

// scopeLocked is the directories shown and those they link into.
func (s *Source) scopeLocked() []string {
	scope := slices.Clone(s.shown)
	for _, d := range s.shown {
		scope = append(scope, s.links[d]...)
	}
	slices.Sort(scope)
	return slices.Compact(scope)
}

// rescope watches scope and answers the directories it added; the watch's
// verdict is the files' told, and dark and what was told of each file follow
// the view, so a directory that leaves it darkens the files no more and
// leaves nothing kept for it.
func (s *Source) rescope(scope []string) []string {
	added, err := s.watch.set(scope)
	why := ""
	if err != nil {
		why = err.Error()
	}
	s.toldMu.Lock()
	maps.DeleteFunc(s.told, func(path, _ string) bool { return !slices.Contains(scope, filepath.Dir(path)) })
	s.toldMu.Unlock()
	s.setHealth(func() {
		s.liveOff = why
		for d := range s.dark {
			if !slices.Contains(scope, d) {
				delete(s.dark, d)
			}
		}
		maps.DeleteFunc(s.refused, func(d, _ string) bool {
			return !slices.Contains(scope, d) && !slices.Contains(scope, filepath.Dir(d))
		})
	})
	return added
}

// linkedFrom is the directories whose last listing links into dir.
func (s *Source) linkedFrom(dir string) []string {
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	var out []string
	for holder, into := range s.links {
		if slices.Contains(into, dir) {
			out = append(out, holder)
		}
	}
	return out
}

// check lists each directory and announces those whose listing moved. One
// that will not list has moved, since what a client holds of it is unknown.
func (s *Source) check(dirs []string) {
	var moved []string
	for _, d := range dirs {
		l, err := s.synthesize(d)
		if err != nil || s.settle(l, true) {
			moved = append(moved, d)
		}
	}
	s.announce(moved)
}

// announce tells each directory and every directory linking into one, each
// once.
func (s *Source) announce(dirs []string) {
	var grids []string
	for _, d := range dirs {
		grids = append(grids, d)
		grids = append(grids, s.linkedFrom(d)...)
	}
	slices.Sort(grids)
	for _, g := range slices.Compact(grids) {
		s.publishGridChanged(g)
	}
}

// tell is what one closed debounce window owes dir, from one listing of it
// at most, however many of its files changed.
func (s *Source) tell(dir string, listing bool, files []string) {
	w := &windowRead{s: s, dir: dir}
	if listing {
		w.check()
	}
	slices.Sort(files)
	for _, f := range files {
		s.applyFile(w, f)
	}
}

// windowRead is one window's listing of its directory, read when first
// needed, and whether it was checked.
type windowRead struct {
	s       *Source
	dir     string
	l       *listing
	err     error
	checked bool
}

func (w *windowRead) listing() (*listing, error) {
	if w.l == nil && w.err == nil {
		w.l, w.err = w.s.synthesize(w.dir)
	}
	return w.l, w.err
}

// check is Source.check of the window's listing, once.
func (w *windowRead) check() {
	if w.checked {
		return
	}
	w.checked = true
	if l, err := w.listing(); err != nil || w.s.settle(l, true) {
		w.s.announce([]string{w.dir})
	}
}

// applyFile tells every client one file changed in place: its row as GetGrid
// serves it, flagged content_changed, because a file's row carries no
// version that could say so. A file the listing does not hold is the
// listing's to answer.
func (s *Source) applyFile(w *windowRead, path string) {
	de, err := statEntry(path)
	if err != nil || de.kind == diskDir {
		return
	}
	e := s.entryOf(de)
	if !s.fresh(path, e.stamp) {
		return
	}
	t, err := s.fileTile(w, e)
	if err != nil || t == nil {
		w.check()
		return
	}
	s.hub.Publish(&pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: t, ContentChanged: true}}})
}

// fresh notes the stamp path is told under and reports whether it moved, so
// one change told twice does not retire the screenshot a view took since.
// A file with no stamp is always fresh.
func (s *Source) fresh(path, stamp string) bool {
	if stamp == "" {
		return true
	}
	s.toldMu.Lock()
	defer s.toldMu.Unlock()
	if s.told[path] == stamp {
		return false
	}
	if s.told == nil {
		s.told = map[string]string{}
	}
	s.told[path] = stamp
	return true
}

// fileTile is e's tile in w's directory. A row keeps its own placement, so e
// alone builds it and refreshes its snapshot as a listing would, and retires
// a page's screenshot the change left behind; an untouched file's placement
// flows from the whole listing, the window's.
func (s *Source) fileTile(w *windowRead, e listed) (*pb.Tile, error) {
	dir := w.dir
	gid, _, err := s.mem.LookupGrid(dir)
	if err != nil {
		return nil, err
	}
	if _, minted, err := s.mem.LiveTileID(gid, e.Key); err != nil || !minted {
		if err != nil {
			return nil, err
		}
		l, err := w.listing()
		if err != nil {
			return nil, err
		}
		return l.tile(e.Key), nil
	}
	one := []listed{e}
	if err := s.mem.Refresh(gid, storeEntries(one)); err != nil {
		return nil, err
	}
	rows, err := s.mem.Overlay(gid, storeEntries(one))
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(rows, func(r store.FileTile) bool { return r.Key == e.Key })
	if i < 0 {
		return nil, nil
	}
	r := rows[i]
	if pagePreviewStale(e, r.Tile) {
		if err := s.mem.DropURLPreview(r.ID); err != nil {
			return nil, err
		}
		r.PreviewBlobId = 0
	}
	tiles, err := s.buildTiles(dir, []store.FileTile{r}, one)
	if err != nil {
		return nil, err
	}
	return tiles[0], nil
}

// watcher owns the one OS notification instance, which exists only while it
// watches a directory.
type watcher struct {
	s      *Source
	window time.Duration
	// add is the OS watch call; a test injects the limit the OS refuses with.
	add func(fsw *fsnotify.Watcher, dir string) error

	mu      sync.Mutex
	fsw     *fsnotify.Watcher
	scope   map[string]bool
	watched map[string]bool
	pending map[string]*window
	// failed is the OS's last refusal, which the next event delivered ends,
	// or the end of the instance it was reported on.
	failed string
	// closed is Close's: nothing is watched or owed after it.
	closed bool
	// running is every reader and every window's tell, which Close waits for.
	running sync.WaitGroup
}

// window is one directory's changes while its debounce is open.
type window struct {
	listing bool
	files   map[string]bool
	timer   *time.Timer
}

func newWatcher(s *Source) *watcher {
	return &watcher{s: s, window: debounceWindow, add: (*fsnotify.Watcher).Add,
		scope: map[string]bool{}, watched: map[string]bool{}, pending: map[string]*window{}}
}

// set watches scope, dropping every watch outside it, and answers the
// directories it added, and those it watches only now, one that would not
// read or be watched before. A directory the OS refuses is the error, and
// the rest stay watched.
func (w *watcher) set(scope []string) (added []string, err error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil, nil
	}
	next := map[string]bool{}
	for _, d := range scope {
		next[d] = true
	}
	for d := range w.watched {
		if !next[d] {
			_ = w.fsw.Remove(d)
			delete(w.watched, d)
		}
	}
	for _, d := range scope {
		was := w.watched[d]
		err = errors.Join(err, w.watchDir(d))
		if !w.scope[d] || !was && w.watched[d] {
			added = append(added, d)
		}
	}
	w.scope = next
	closing := w.detachIfIdle()
	if err == nil && w.failed != "" {
		err = errors.New(w.failed)
	}
	w.mu.Unlock()
	closeWatcher(closing)
	return added, err
}

// watchDir puts dir under an OS watch. One missing or unreadable is not
// watched and is no error: its listing answers for it.
func (w *watcher) watchDir(dir string) error {
	if w.watched[dir] {
		return nil
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil
	}
	if w.fsw == nil {
		fsw, err := fsnotify.NewWatcher()
		if err != nil {
			return verdict(dir, err)
		}
		w.fsw = fsw
		w.running.Add(1)
		go func() {
			defer w.running.Done()
			w.run(fsw)
		}()
	}
	if err := w.add(w.fsw, dir); err != nil {
		return verdict(dir, err)
	}
	w.watched[dir] = true
	return nil
}

// verdict is what an OS refusal to watch dir means: running out of watches is
// the sentence telling the user how to raise the limit, and listings still
// answer.
func verdict(dir string, err error) error {
	switch {
	case errors.Is(err, iofs.ErrNotExist), errors.Is(err, iofs.ErrPermission):
		return nil
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE):
		return status.Errorf(codes.ResourceExhausted,
			"the OS refused another change watch at %s (%v): raise its watch limit (fs.inotify.max_user_watches on Linux, the open-file limit on macOS) or show fewer directories", dir, err)
	}
	return status.Errorf(codes.Internal, "files: watch %s: %v", dir, err)
}

// detachIfIdle takes the OS instance away once nothing is watched, for the
// caller to close outside mu: closing waits for the reader, which may be
// waiting on mu.
func (w *watcher) detachIfIdle() *fsnotify.Watcher {
	if len(w.watched) > 0 || w.fsw == nil {
		return nil
	}
	fsw := w.fsw
	w.fsw, w.failed = nil, ""
	return fsw
}

func closeWatcher(fsw *fsnotify.Watcher) {
	if fsw != nil {
		_ = fsw.Close()
	}
}

// close stops every open window and the OS instance, and waits for the
// reader and any tell already under way.
func (w *watcher) close() {
	w.mu.Lock()
	w.closed = true
	for _, win := range w.pending {
		if win.timer.Stop() {
			w.running.Done()
		}
	}
	fsw := w.fsw
	w.fsw, w.scope, w.watched, w.pending = nil, map[string]bool{}, map[string]bool{}, map[string]*window{}
	w.mu.Unlock()
	closeWatcher(fsw)
	w.running.Wait()
}

func (w *watcher) run(fsw *fsnotify.Watcher) {
	for {
		select {
		case ev, ok := <-fsw.Events:
			if !ok {
				return
			}
			w.event(fsw, ev)
		case err, ok := <-fsw.Errors:
			if !ok {
				return
			}
			w.eventError(fsw, err)
		}
	}
}

// event maps one OS event to what it touches. A name that came, went or
// moved, or whose mode changed (an image's mtime is its picture's stamp),
// touches its directory's listing; bytes written, or a file created over its
// name as an editor's save does, touch that file. A watched directory that
// vanished or appeared touches its own listing.
func (w *watcher) event(fsw *fsnotify.Watcher, ev fsnotify.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fsw != fsw {
		return
	}
	if w.failed != "" {
		w.failed = ""
		w.s.noteWatch("")
	}
	path := filepath.Clean(ev.Name)
	switch {
	case w.watched[path] && ev.Has(fsnotify.Remove|fsnotify.Rename):
		_ = fsw.Remove(path)
		delete(w.watched, path)
		w.touch(path, true, "")
	case !w.watched[path] && ev.Has(fsnotify.Create) && w.scope[path]:
		if err := w.watchDir(path); err != nil {
			w.failed = err.Error()
			w.s.noteWatch(w.failed)
		}
		w.touch(path, true, "")
	}
	if ev.Has(fsnotify.Chmod) {
		w.s.noteRefusal(path, nil)
	}
	parent := filepath.Dir(path)
	if !w.watched[parent] {
		return
	}
	file := ""
	if ev.Has(fsnotify.Write | fsnotify.Create) {
		file = path
	}
	w.touch(parent, ev.Has(fsnotify.Create|fsnotify.Remove|fsnotify.Rename|fsnotify.Chmod), file)
}

// eventError is the OS's own verdict. An overflow lost events, so every
// watched directory and every file in it is told again; any other leaves the
// files not told until the next event arrives.
func (w *watcher) eventError(fsw *fsnotify.Watcher, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fsw != fsw {
		return
	}
	if !errors.Is(err, fsnotify.ErrEventOverflow) {
		w.failed = "change notifications failed: " + err.Error()
		w.s.noteWatch(w.failed)
		return
	}
	for d := range w.watched {
		ents, _ := readDir(d)
		for _, e := range ents {
			w.touch(d, true, e.path)
		}
		w.touch(d, true, "")
	}
}

// touch adds to dir's debounce window, opening one if none is open. Callers
// hold mu.
func (w *watcher) touch(dir string, listing bool, file string) {
	if w.closed {
		return
	}
	win := w.pending[dir]
	if win == nil {
		win = &window{files: map[string]bool{}}
		w.pending[dir] = win
		w.running.Add(1)
		win.timer = time.AfterFunc(w.window, func() {
			defer w.running.Done()
			w.owe(dir)
		})
	}
	win.listing = win.listing || listing
	if file != "" {
		win.files[file] = true
	}
}

// relist touches dir's listing, as a change heard in it would.
func (w *watcher) relist(dir string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.touch(dir, true, "")
}

// owe closes dir's window and tells what it gathered, while dir is still
// watched.
func (w *watcher) owe(dir string) {
	w.mu.Lock()
	win := w.pending[dir]
	delete(w.pending, dir)
	wanted := w.scope[dir]
	w.mu.Unlock()
	if win == nil || !wanted {
		return
	}
	files := make([]string, 0, len(win.files))
	for f := range win.files {
		files = append(files, f)
	}
	w.s.tell(dir, win.listing, files)
}
