package cache

import "testing"

// A room is a memory when the source serving it is not answering. That is one
// fact — the source's health — and the bar's chip is its projection onto one
// room, so the join is the same one a resync is scoped by.
func TestAGridIsAMemoryWhenItsSourceIsDark(t *testing.T) {
	c := seedSources(t)
	for _, id := range c.KnownGridIDs() {
		if c.SourceDark(id) {
			t.Fatalf("%s is a memory before any health event", id)
		}
	}

	c.NoteHealth(conn, false)
	for _, id := range []string{farHome + "/1", farPl + "/4"} {
		if !c.SourceDark(id) {
			t.Errorf("%s rides the dark connection and is a memory", id)
		}
	}
	for _, id := range []string{node + "/7", fsPl + "/1", fsPl + "/12"} {
		if c.SourceDark(id) {
			t.Errorf("%s is served by nobody who flapped", id)
		}
	}

	// The recovery is the same fact arriving the other way round.
	c.NoteHealth(conn, true)
	for _, id := range c.KnownGridIDs() {
		if c.SourceDark(id) {
			t.Errorf("%s stayed a memory after its source came back", id)
		}
	}
}

// Darkness is per source: a plugin going dark says nothing about the grids
// another source answers for, and says it about every grid of its own.
func TestDarknessNamesOneSource(t *testing.T) {
	c := seedSources(t)
	c.NoteHealth(fsPl, false)
	if !c.SourceDark(fsPl+"/1") || !c.SourceDark(fsPl+"/12") {
		t.Error("both of the plugin's grids are memories")
	}
	if c.SourceDark(farHome + "/1") {
		t.Error("a local plugin's darkness reached a grid through the connection")
	}

	// Two sources dark at once, and each one clears alone.
	c.NoteHealth(conn, false)
	c.NoteHealth(fsPl, true)
	if c.SourceDark(fsPl + "/1") {
		t.Error("the plugin came back")
	}
	if !c.SourceDark(farHome + "/1") {
		t.Error("the connection is still dark")
	}
}

// The empty uuid names no source. EverySource is what a cursorless gap means
// for a resync, and reading it here would call every room in the client a
// memory on one malformed event.
func TestAnUnnamedSourceDarkensNothing(t *testing.T) {
	c := seedSources(t)
	c.NoteHealth(EverySource, false)
	for _, id := range c.KnownGridIDs() {
		if c.SourceDark(id) {
			t.Errorf("%s went dark on a health event that named no source", id)
		}
	}
	if c.SourceDark("") {
		t.Error("no id is no room")
	}
}

// NoteHealth answers what the source was before, so the caller can tell a
// move of the healthy bit from an event that repeats it (events.ReactHealth).
func TestNoteHealthAnswersWhatItWas(t *testing.T) {
	c := seedSources(t)
	steps := []struct {
		healthy, wasDark bool
	}{{true, false}, {false, false}, {false, true}, {true, true}, {true, false}}
	for i, s := range steps {
		if got := c.NoteHealth(fsPl, s.healthy); got != s.wasDark {
			t.Errorf("step %d: wasDark = %v, want %v", i, got, s.wasDark)
		}
	}
}

// A grid is current when the cache holds it and its source is answering: the
// event stream keeps that one true, so navigation asks for nothing in it.
func TestAGridIsCurrentWhenHeldFromAnAnsweringSource(t *testing.T) {
	c := seedSources(t)
	c.NoteHealth(conn, false)
	cur := c.Current()
	for _, id := range []string{node + "/7", fsPl + "/1", fsPl + "/12"} {
		if !cur[id] {
			t.Errorf("%s is held from an answering source and not current", id)
		}
	}
	for _, id := range []string{farHome + "/1", farPl + "/4"} {
		if cur[id] {
			t.Errorf("%s rides the dark connection and is current", id)
		}
	}
	if cur[node+"/never-held"] {
		t.Error("a grid the cache never held is current")
	}
	c.NoteHealth(conn, true)
	if !c.Current()[farHome+"/1"] {
		t.Error("the far home stayed not current after its source came back")
	}
}

// A source that lists but cannot watch is not dark, but nothing will tell this
// client its changes, so its grids are not current: every look reads them.
func TestAGridFromASourceWithLiveUpdatesOffIsNotCurrent(t *testing.T) {
	c := seedSources(t)
	c.NoteLiveUpdates(fsPl, false)
	cur := c.Current()
	for _, id := range []string{fsPl + "/1", fsPl + "/12"} {
		if cur[id] {
			t.Errorf("%s has no live updates and is current", id)
		}
		if c.SourceDark(id) {
			t.Errorf("%s still lists, so it is not dark", id)
		}
	}
	if !cur[node+"/7"] || !cur[farHome+"/1"] {
		t.Error("another source's live updates went off with the plugin's")
	}
	c.NoteLiveUpdates(fsPl, true)
	if !c.Current()[fsPl+"/1"] {
		t.Error("the plugin's grid stayed not current after its live updates came back")
	}
}
