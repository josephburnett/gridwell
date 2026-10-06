package bartitle

import (
	"testing"

	"github.com/josephburnett/gridwell/client/door"
)

// The bar draws Label and the right-click edits Rename, so a row here pins the
// name shown and the row it edits together.
func TestDecide(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want Verdict
	}{
		{
			"a url descent names its tile",
			Input{Descent: true, Descended: true, DescendedName: "docs"},
			Verdict{Rename: RenameDescent, Label: "docs", Editable: true},
		},
		{
			"a nameless descent still takes the rename",
			Input{Descent: true, Descended: true},
			Verdict{Rename: RenameDescent, Label: unnamed, Editable: true, Muted: true},
		},
		{
			"a text tile's name is derived, so it is read-only",
			Input{Descent: true, Descended: true, DescendedText: true, DescendedName: "notes"},
			Verdict{Label: "notes"},
		},
		{
			"a nameless text tile reads unnamed",
			Input{Descent: true, Descended: true, DescendedText: true},
			Verdict{Label: unnamed, Muted: true},
		},
		{
			"a descent whose row is not cached yet reads unnamed",
			Input{Descent: true, DescendedName: "stale"},
			Verdict{Label: unnamed, Muted: true},
		},
		{
			"a visit that may be ephemeral is not renamable, but still says its name",
			Input{Descent: true, Descended: true, PossiblyEphemeral: true, DescendedName: "example.com"},
			Verdict{Label: "example.com"},
		},
		{
			"a known ephemeral visit says so",
			Input{Descent: true, Descended: true, PossiblyEphemeral: true,
				CertainlyEphemeral: true, DescendedName: "example.com"},
			Verdict{Label: "ephemeral", Muted: true},
		},
		{
			"a well's grid names the well it was entered through",
			Input{AtAnchor: true, Door: door.Well, DoorName: "inbox"},
			Verdict{Rename: RenameDoor, Label: "inbox", Editable: true},
		},
		{
			"a nameless well still takes the rename",
			Input{AtAnchor: true, Door: door.Well},
			Verdict{Rename: RenameDoor, Label: unnamed, Editable: true, Muted: true},
		},
		{
			"a declared menu entry is config-owned, so it is read-only",
			Input{AtAnchor: true, Door: door.Entry, DoorName: "Feed (hey)"},
			Verdict{Label: "Feed (hey)", Muted: true},
		},
		{
			"a declared root is config-owned too",
			Input{AtAnchor: true, Door: door.Root, DoorName: "fs"},
			Verdict{Label: "fs", Muted: true},
		},
		{
			"a nameless declaration reads unnamed",
			Input{AtAnchor: true, Door: door.Entry},
			Verdict{Label: unnamed, Muted: true},
		},
		{
			"no door at all falls through to the declared label",
			Input{AtAnchor: true, ConfigLabel: "gitlab"},
			Verdict{Label: "gitlab", Muted: true},
		},
		{
			"no door and no declaration reads unnamed",
			Input{AtAnchor: true},
			Verdict{Label: unnamed, Muted: true},
		},
		{
			"a well row inside a grid names that row",
			Input{Parent: true, ParentName: "projects"},
			Verdict{Rename: RenameParent, Label: "projects", Editable: true},
		},
		{
			"a nameless well row still takes the rename",
			Input{Parent: true},
			Verdict{Rename: RenameParent, Label: unnamed, Editable: true, Muted: true},
		},
		{
			"an uncached parent shows the plugin's declared label",
			Input{ConfigLabel: "fs"},
			Verdict{Label: "fs", Muted: true},
		},
		{
			"an uncached parent with no declaration reads unnamed",
			Input{},
			Verdict{Label: unnamed, Muted: true},
		},
	}
	for _, c := range cases {
		if got := Decide(c.in); got != c.want {
			t.Errorf("%s: Decide(%+v) = %+v, want %+v", c.name, c.in, got, c.want)
		}
	}
}

// The descent is the outer gate, so a level fact left over from the grid below
// cannot rename the room from inside a tile.
func TestDecideDescentBeatsLevel(t *testing.T) {
	in := Input{Descent: true, Descended: true, DescendedText: true, DescendedName: "notes",
		AtAnchor: true, Door: door.Well, DoorName: "inbox", Parent: true, ParentName: "projects"}
	if got := Decide(in); got != (Verdict{Label: "notes"}) {
		t.Fatalf("Decide(descent over level) = %+v, want the descended tile's name", got)
	}
}

// A pane standing on the level's own grid has no path tail, so the door
// answers even where a stale parent fact is set.
func TestDecideAnchorBeatsParent(t *testing.T) {
	in := Input{AtAnchor: true, Door: door.Entry, DoorName: "Feed (hey)",
		Parent: true, ParentName: "projects"}
	if got := Decide(in); got != (Verdict{Label: "Feed (hey)", Muted: true}) {
		t.Fatalf("Decide(anchor over parent) = %+v, want the door's label", got)
	}
}

// A grid's source label follows the room's name on every arm a pane stands on
// a grid, and never inside a tile, where the level's facts are stale.
func TestDecideSourceLabel(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want string
	}{
		{"a collection names its source after its door",
			Input{AtAnchor: true, Door: door.Entry, DoorName: "Inbox (hey)", SourceLabel: "Inbox · 12 unread"},
			"Inbox (hey) — Inbox · 12 unread"},
		{"a well row inside a plugin grid names its directory",
			Input{Parent: true, ParentName: "src", SourceLabel: "/srv/src"},
			"src — /srv/src"},
		{"a nameless room still says its source",
			Input{AtAnchor: true, SourceLabel: "/srv"},
			"unnamed — /srv"},
		{"a source that only repeats the name adds nothing",
			Input{AtAnchor: true, Door: door.Entry, DoorName: "pages", SourceLabel: "pages"},
			"pages"},
		{"a grid whose source says nothing shows the name alone",
			Input{AtAnchor: true, Door: door.Entry, DoorName: "Feed (hey)"},
			"Feed (hey)"},
		{"inside a tile the source does not show",
			Input{Descent: true, Descended: true, DescendedName: "notes", SourceLabel: "/srv"},
			"notes"},
	}
	for _, c := range cases {
		if got := Decide(c.in).Text(); got != c.want {
			t.Errorf("%s: Decide(%+v).Text() = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// The source label is shown, never edited: the rename still names the room's
// own row with its own name.
func TestDecideSourceLabelLeavesTheRenameAlone(t *testing.T) {
	v := Decide(Input{AtAnchor: true, Door: door.Well, DoorName: "inbox", SourceLabel: "/srv/inbox"})
	if v.Rename != RenameDoor || v.Label != "inbox" || !v.Editable {
		t.Fatalf("Decide = %+v, want the well's rename on its own name", v)
	}
}
