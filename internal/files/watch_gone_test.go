package files

import (
	"os"
	"slices"
	"testing"
	"time"
)

// awaitWatched waits for the watch set to be want, in any order.
func (f *fixture) awaitWatched(want ...string) {
	f.t.Helper()
	slices.Sort(want)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := f.watched()
		slices.Sort(got)
		if slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("watched %v, want %v", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A shown directory whose parent nobody shows, deleted and made again, is
// watched again and told: while it is gone the nearest directory above it
// is watched for its return, and only then.
func TestARecreatedDirectoryShownAloneIsWatchedAgain(t *testing.T) {
	f := newFixture(t)
	dir := f.mkdir("a/dist")
	f.grid(dir)
	ev := f.events()
	f.show(dir)
	f.awaitWatched(dir)
	if err := os.RemoveAll(f.path("a")); err != nil {
		t.Fatal(err)
	}
	await(t, ev, "the vanished directory", gridChanged(dir))
	f.awaitWatched(f.root)
	f.mkdir("a/dist")
	f.awaitWatched(dir)
	f.grid(dir)
	f.write("a/dist/x.txt", "x")
	await(t, ev, "the recreated directory's change", gridChanged(dir))
	if h := f.s.health(); h.liveOff != "" {
		t.Errorf("health %+v, want told", h)
	}
}

// A directory shown before it exists is told when it is made, and watched.
func TestADirectoryShownBeforeItIsMadeIsWatched(t *testing.T) {
	f := newFixture(t)
	dir := f.path("later")
	ev := f.events()
	f.show(dir)
	await(t, ev, "the view's check of a directory not there", gridChanged(dir))
	f.awaitWatched(f.root)
	f.mkdir("later")
	await(t, ev, "the directory made", gridChanged(dir))
	f.awaitWatched(dir)
	f.grid(dir)
	f.write("later/x.txt", "x")
	await(t, ev, "the new directory's change", gridChanged(dir))
}
