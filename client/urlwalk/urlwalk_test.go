package urlwalk

import (
	"reflect"
	"testing"
)

// world is a tiny in-memory grid set for tests: gridID -> (tileID -> Tile).
type world map[string]map[string]Tile

func (w world) lookup() GridLookup {
	return func(gid string) (map[string]Tile, bool) {
		g, ok := w[gid]
		return g, ok
	}
}

// lookupFailingGrid wraps a world's lookup so fetches for badGid fail.
func (w world) lookupFailingGrid(badGid string) GridLookup {
	return func(gid string) (map[string]Tile, bool) {
		if gid == badGid {
			return nil, false
		}
		g, ok := w[gid]
		return g, ok
	}
}

func TestWalkEmpty(t *testing.T) {
	w := world{"1": {}}
	path, file := Walk("1", nil, w.lookup())
	if len(path) != 0 || file != "" {
		t.Errorf("empty: path=%v file=%q", path, file)
	}
}

func TestWalkDoorChain(t *testing.T) {
	// root grid "1" has door "10" -> grid "2"; grid "2" has door "20" -> grid "3".
	w := world{
		"1": {"10": {IsDoor: true, ChildGridID: "2"}},
		"2": {"20": {IsDoor: true, ChildGridID: "3"}},
		"3": {},
	}
	path, file := Walk("1", []string{"10", "20"}, w.lookup())
	if !reflect.DeepEqual(path, []string{"10", "20"}) {
		t.Errorf("path = %v, want [10 20]", path)
	}
	if file != "" {
		t.Errorf("file = %q, want empty (grid leaf)", file)
	}
}

func TestWalkContentLeaf(t *testing.T) {
	w := world{
		"1": {"10": {IsDoor: true, ChildGridID: "2"}},
		"2": {"99": {IsContent: true}},
	}
	path, file := Walk("1", []string{"10", "99"}, w.lookup())
	if !reflect.DeepEqual(path, []string{"10"}) {
		t.Errorf("path = %v, want [10]", path)
	}
	if file != "99" {
		t.Errorf("file = %q, want 99", file)
	}
}

func TestWalkSkipsMissingID(t *testing.T) {
	// "9999" is not in grid "1", so the walk stays there and resolves the door
	// "10" next.
	w := world{
		"1": {"10": {IsDoor: true, ChildGridID: "2"}},
		"2": {"15": {IsContent: true}},
	}
	path, file := Walk("1", []string{"9999", "10", "15"}, w.lookup())
	if !reflect.DeepEqual(path, []string{"10"}) {
		t.Errorf("path = %v, want [10]", path)
	}
	if file != "15" {
		t.Errorf("file = %q, want 15", file)
	}
}

func TestWalkContentMidPathIgnored(t *testing.T) {
	// A content tile that is not last is skipped.
	w := world{
		"1": {"5": {IsContent: true}, "10": {IsDoor: true, ChildGridID: "2"}},
		"2": {},
	}
	path, file := Walk("1", []string{"5", "10"}, w.lookup())
	if !reflect.DeepEqual(path, []string{"10"}) {
		t.Errorf("path = %v, want [10] (mid-path content 5 ignored)", path)
	}
	if file != "" {
		t.Errorf("file = %q, want empty", file)
	}
}

func TestWalkStopsOnFailedFetch(t *testing.T) {
	// Grid "2" fails to load, so the walk ends with the door "10" and no file.
	// A failed read never invents more path.
	w := world{
		"1": {"10": {IsDoor: true, ChildGridID: "2"}},
		"2": {"20": {IsDoor: true, ChildGridID: "3"}},
		"3": {},
	}
	path, file := Walk("1", []string{"10", "20"}, w.lookupFailingGrid("2"))
	if !reflect.DeepEqual(path, []string{"10"}) {
		t.Errorf("path = %v, want [10] (grid 2 fetch failed)", path)
	}
	if file != "" {
		t.Errorf("file = %q, want empty", file)
	}
}

func TestWalkRootFetchFails(t *testing.T) {
	w := world{}
	path, file := Walk("1", []string{"10"}, w.lookupFailingGrid("1"))
	if len(path) != 0 || file != "" {
		t.Errorf("root fetch fail: path=%v file=%q, want empty", path, file)
	}
}

func TestWalkWrongGridIDNotFollowed(t *testing.T) {
	// id "10" exists in grid "1" but not in grid "2". After descending, later
	// ids resolve against the child grid, so this "10" is skipped.
	w := world{
		"1": {"10": {IsDoor: true, ChildGridID: "2"}},
		"2": {"7": {IsContent: true}},
	}
	path, file := Walk("1", []string{"10", "10", "7"}, w.lookup())
	if !reflect.DeepEqual(path, []string{"10"}) {
		t.Errorf("path = %v, want [10]", path)
	}
	if file != "7" {
		t.Errorf("file = %q, want 7", file)
	}
}
