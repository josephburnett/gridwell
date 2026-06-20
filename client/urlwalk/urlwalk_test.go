package urlwalk

import (
	"reflect"
	"testing"
)

// world is a tiny in-memory grid set for tests: gridID -> (tileID -> Tile).
type world map[int64]map[int64]Tile

func (w world) lookup() GridLookup {
	return func(gid int64) (map[int64]Tile, bool) {
		g, ok := w[gid]
		return g, ok
	}
}

// failAfter wraps a world's lookup so the Nth distinct grid fetch fails,
// simulating a grid that can't be loaded.
func (w world) lookupFailingGrid(badGid int64) GridLookup {
	return func(gid int64) (map[int64]Tile, bool) {
		if gid == badGid {
			return nil, false
		}
		g, ok := w[gid]
		return g, ok
	}
}

func TestWalkEmpty(t *testing.T) {
	w := world{1: {}}
	path, file := Walk(1, nil, w.lookup())
	if len(path) != 0 || file != 0 {
		t.Errorf("empty: path=%v file=%d", path, file)
	}
}

func TestWalkWellChain(t *testing.T) {
	// root grid 1 has well 10 -> grid 2; grid 2 has well 20 -> grid 3.
	w := world{
		1: {10: {IsWell: true, ChildGridID: 2}},
		2: {20: {IsWell: true, ChildGridID: 3}},
		3: {},
	}
	path, file := Walk(1, []int64{10, 20}, w.lookup())
	if !reflect.DeepEqual(path, []int64{10, 20}) {
		t.Errorf("path = %v, want [10 20]", path)
	}
	if file != 0 {
		t.Errorf("file = %d, want 0 (grid leaf)", file)
	}
}

func TestWalkContentLeaf(t *testing.T) {
	w := world{
		1: {10: {IsWell: true, ChildGridID: 2}},
		2: {99: {IsContent: true}},
	}
	path, file := Walk(1, []int64{10, 99}, w.lookup())
	if !reflect.DeepEqual(path, []int64{10}) {
		t.Errorf("path = %v, want [10]", path)
	}
	if file != 99 {
		t.Errorf("file = %d, want 99", file)
	}
}

func TestWalkSkipsMissingID(t *testing.T) {
	// 9999 is not in grid 1: it's skipped, the walk stays in grid 1 and
	// resolves the next id (the well 10).
	w := world{
		1: {10: {IsWell: true, ChildGridID: 2}},
		2: {15: {IsContent: true}},
	}
	path, file := Walk(1, []int64{9999, 10, 15}, w.lookup())
	if !reflect.DeepEqual(path, []int64{10}) {
		t.Errorf("path = %v, want [10]", path)
	}
	if file != 15 {
		t.Errorf("file = %d, want 15", file)
	}
}

func TestWalkContentMidPathIgnored(t *testing.T) {
	// A content tile that isn't last is nonsense: skip it and keep walking.
	w := world{
		1: {5: {IsContent: true}, 10: {IsWell: true, ChildGridID: 2}},
		2: {},
	}
	path, file := Walk(1, []int64{5, 10}, w.lookup())
	if !reflect.DeepEqual(path, []int64{10}) {
		t.Errorf("path = %v, want [10] (mid-path content 5 ignored)", path)
	}
	if file != 0 {
		t.Errorf("file = %d, want 0", file)
	}
}

func TestWalkStopsOnFailedFetch(t *testing.T) {
	// Grid 2 fails to load: the walk ends with the path resolved so far
	// (the well 10) and no file. A failed read never invents more path.
	w := world{
		1: {10: {IsWell: true, ChildGridID: 2}},
		2: {20: {IsWell: true, ChildGridID: 3}},
		3: {},
	}
	path, file := Walk(1, []int64{10, 20}, w.lookupFailingGrid(2))
	if !reflect.DeepEqual(path, []int64{10}) {
		t.Errorf("path = %v, want [10] (grid 2 fetch failed)", path)
	}
	if file != 0 {
		t.Errorf("file = %d, want 0", file)
	}
}

func TestWalkRootFetchFails(t *testing.T) {
	w := world{}
	path, file := Walk(1, []int64{10}, w.lookupFailingGrid(1))
	if len(path) != 0 || file != 0 {
		t.Errorf("root fetch fail: path=%v file=%d, want empty", path, file)
	}
}

func TestWalkWrongGridIDNotFollowed(t *testing.T) {
	// id 10 exists in grid 1 but NOT in grid 2; descending into a child
	// grid must resolve subsequent ids against the child (grid 2), not
	// re-find 10 in the parent. Here grid 2 lacks 10, so it's skipped.
	w := world{
		1: {10: {IsWell: true, ChildGridID: 2}},
		2: {7: {IsContent: true}},
	}
	path, file := Walk(1, []int64{10, 10, 7}, w.lookup())
	if !reflect.DeepEqual(path, []int64{10}) {
		t.Errorf("path = %v, want [10]", path)
	}
	if file != 7 {
		t.Errorf("file = %d, want 7", file)
	}
}
