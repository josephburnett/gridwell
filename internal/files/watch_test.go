package files

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

func (f *fixture) show(dirs ...string) {
	f.t.Helper()
	ids := make([]string, len(dirs))
	for i, d := range dirs {
		ids[i] = idOf(d)
	}
	if _, err := f.s.SetView(context.Background(), &pb.SetViewRequest{ShownGridIds: ids}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) watched() []string {
	f.s.watch.mu.Lock()
	defer f.s.watch.mu.Unlock()
	var out []string
	for d := range f.s.watch.watched {
		out = append(out, d)
	}
	return out
}

func tileChanged(path string) func(*pb.Event) bool {
	return func(ev *pb.Event) bool { return ev.GetTileChanged().GetTile().GetId() == idOf(path) }
}

// quiet fails on any event matching match within the window.
func quiet(t *testing.T, ch <-chan *pb.Event, what string, match func(*pb.Event) bool) {
	t.Helper()
	deadline := time.After(3 * debounceWindow)
	for {
		select {
		case ev := <-ch:
			if match(ev) {
				t.Fatalf("%s: %v", what, ev)
			}
		case <-deadline:
			return
		}
	}
}

// awaitAll answers once every match has accepted an event, in any order.
func awaitAll(t *testing.T, ch <-chan *pb.Event, matches map[string]func(*pb.Event) bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for len(matches) > 0 {
		select {
		case ev := <-ch:
			for what, m := range matches {
				if m(ev) {
					delete(matches, what)
				}
			}
		case <-deadline:
			t.Fatalf("none of %v within 5s", slices.Collect(maps.Keys(matches)))
		}
	}
}

// A name that comes into a shown directory is its GridChanged, a burst told
// once; a directory nobody shows is not watched.
func TestAShownDirectoryTellsItsChanges(t *testing.T) {
	f := newFixture(t)
	hidden := f.mkdir("hidden")
	f.grid(f.root)
	ev := f.events()
	f.show(f.root)
	if got := f.watched(); len(got) != 1 || got[0] != f.root {
		t.Fatalf("watched %v", got)
	}
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		f.write(n, n)
	}
	await(t, ev, "the root's change", gridChanged(f.root))
	quiet(t, ev, "a burst told twice", gridChanged(f.root))
	f.write("hidden/x.txt", "x")
	quiet(t, ev, "a hidden directory told", gridChanged(hidden))
	f.show()
	if got := f.watched(); len(got) != 0 {
		t.Errorf("an empty view still watches %v", got)
	}
	f.write("d.txt", "d")
	quiet(t, ev, "an unshown directory told", gridChanged(f.root))
}

// A file written in place is its TileChanged, flagged content_changed and
// carrying the new stamp; a save by rename is one too.
func TestAWrittenFileTellsItsTile(t *testing.T) {
	f := newFixture(t)
	p := f.write("a.md", "one")
	before := tileIn(t, f.grid(f.root), p)
	ev := f.events()
	f.show(f.root)
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(p, []byte("two!"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := await(t, ev, "the file's change", tileChanged(p)).GetTileChanged()
	if !got.ContentChanged || got.Tile.ContentStamp == before.ContentStamp {
		t.Errorf("tile changed = %+v", got)
	}
	tmp := f.write(".a.md.tmp", "three")
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
	await(t, ev, "the renamed save", tileChanged(p))
}

// A shown directory that vanishes is told, and one created again is watched
// again.
func TestAVanishedDirectoryIsToldAndWatchedAgain(t *testing.T) {
	f := newFixture(t)
	dir := f.mkdir("dir")
	f.grid(f.root)
	f.grid(dir)
	ev := f.events()
	f.show(f.root, dir)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	// The root's listing moved too; a mkdir inside its window would leave it
	// as it was, owing nothing.
	awaitAll(t, ev, map[string]func(*pb.Event) bool{
		"the vanished directory": gridChanged(dir), "the root without it": gridChanged(f.root)})
	f.mkdir("dir")
	await(t, ev, "the root's new name", gridChanged(f.root))
	deadline := time.Now().Add(5 * time.Second)
	for len(f.watched()) != 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(f.watched()) != 2 {
		t.Fatalf("watched %v", f.watched())
	}
	f.grid(dir)
	f.write("dir/x.txt", "x")
	await(t, ev, "the recreated directory's change", gridChanged(dir))
}

// A shown directory's symlinks to files watch the directories they land in,
// and a change there tells the holder too.
func TestALinkWatchesWhereItLands(t *testing.T) {
	f := newFixture(t)
	other := f.mkdir("other")
	f.write("other/t.txt", "t")
	if err := os.Symlink(filepath.Join(other, "t.txt"), f.path("link")); err != nil {
		t.Skip("symlinks unsupported")
	}
	f.grid(f.root)
	f.grid(other)
	ev := f.events()
	f.show(f.root)
	if got := f.watched(); len(got) != 2 {
		t.Fatalf("watched %v", got)
	}
	f.write("other/u.txt", "u")
	await(t, ev, "the holder's change", gridChanged(f.root))
}

// A view the OS will not watch leaves the files not told, with the reason,
// until one it takes.
func TestToldFollowsTheWatch(t *testing.T) {
	f := newFixture(t)
	ev := f.events()
	f.s.watch.add = func(*fsnotify.Watcher, string) error { return syscall.ENOSPC }
	f.show(f.root)
	h := await(t, ev, "not told", func(e *pb.Event) bool { return e.GetPluginHealth() != nil }).GetPluginHealth()
	if h.GetTold() || h.LiveUpdatesOff == "" || !h.Healthy {
		t.Errorf("health = %+v", h)
	}
	f.s.watch.add = (*fsnotify.Watcher).Add
	f.show(f.root)
	h = await(t, ev, "told", func(e *pb.Event) bool { return e.GetPluginHealth() != nil }).GetPluginHealth()
	if !h.GetTold() || h.LiveUpdatesOff != "" {
		t.Errorf("health = %+v", h)
	}
}

// An overflow lost events, so every watched directory is listed again and
// what moved is told.
func TestAnOverflowRelistsWhatIsWatched(t *testing.T) {
	f := newFixture(t)
	p := f.write("a.txt", "a")
	f.grid(f.root)
	ev := f.events()
	f.s.watch.add = func(*fsnotify.Watcher, string) error { return nil } // the OS tells nothing
	f.show(f.root)
	f.write("b.txt", "b")
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(p, []byte("aa"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.s.watch.mu.Lock()
	fsw := f.s.watch.fsw
	f.s.watch.mu.Unlock()
	f.s.watch.eventError(fsw, fsnotify.ErrEventOverflow)
	await(t, ev, "the relisting", gridChanged(f.root))
	await(t, ev, "the written file", tileChanged(p))
}

// A view adding a directory lists it again, so a change between a client's
// read and the watch is told.
func TestAViewChecksWhatItAdds(t *testing.T) {
	f := newFixture(t)
	f.grid(f.root)
	f.write("late.txt", "x")
	ev := f.events()
	f.show(f.root)
	await(t, ev, "the change before the watch", gridChanged(f.root))
}

// Dark follows the view: an unreadable directory a session showed and left
// darkens the files no longer.
func TestADarkDirectoryLeftIsNoLongerDark(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	f := newFixture(t)
	locked := f.mkdir("locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	ev := f.events()
	f.grid(f.root)
	f.grid(locked)
	f.show(f.root, locked)
	if h := await(t, ev, "dark", func(e *pb.Event) bool { return e.GetPluginHealth() != nil }).GetPluginHealth(); h.Healthy {
		t.Fatalf("health = %+v", h)
	}
	f.show(f.root)
	if h := await(t, ev, "light", func(e *pb.Event) bool { return e.GetPluginHealth() != nil }).GetPluginHealth(); !h.Healthy {
		t.Errorf("health = %+v", h)
	}
	if h := f.s.health(); h.dark {
		t.Errorf("still dark: %+v", h)
	}
}

// A read that saw the disk before a change whose check settled first is
// announced: the client holds the older listing, and the change's tell came
// before it.
func TestAReadOvertakenByAChangeIsAnnounced(t *testing.T) {
	f := newFixture(t)
	f.grid(f.root)
	ev := f.events()
	read, err := f.s.synthesize(f.root)
	if err != nil {
		t.Fatal(err)
	}
	f.write("late.txt", "x")
	f.s.check([]string{f.root})
	await(t, ev, "the change", gridChanged(f.root))
	f.s.answer(read)
	await(t, ev, "the overtaken read", gridChanged(f.root))
}

// Close ends the watch for good: no reader, no debounce window, and a view
// or a listing after it watches nothing.
func TestCloseLeavesNothingBehind(t *testing.T) {
	f := newFixture(t)
	f.grid(f.root)
	before := runtime.NumGoroutine()
	f.show(f.root)
	f.write("a.txt", "a")
	deadline := time.Now().Add(5 * time.Second)
	for !f.pending() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	f.s.Close()
	f.show(f.root)
	f.grid(f.root)
	if got := f.watched(); len(got) != 0 || f.pending() {
		t.Errorf("after Close: watched %v, a window open %v", got, f.pending())
	}
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Errorf("%d goroutines after Close, %d before New", n, before)
	}
}

func (f *fixture) pending() bool {
	f.s.watch.mu.Lock()
	defer f.s.watch.mu.Unlock()
	return len(f.s.watch.pending) > 0
}
