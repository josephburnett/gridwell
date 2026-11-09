package dragdrop

import "testing"

// A release over a shell pastes whatever the button, and only a template or
// the early arms beat it; a refusal snaps back naming its reason.
func TestDecideDropOntoAShell(t *testing.T) {
	onto := DropInput{Started: true, TileID: "u/1", HasTarget: true, OntoShell: true}
	for _, c := range []struct {
		name string
		in   func(DropInput) DropInput
		want DropAction
		why  string
	}{
		{"a left-drag pastes", func(in DropInput) DropInput { return in }, DropPaste, ""},
		{"a right-drag pastes", func(in DropInput) DropInput { in.Intent = IntentCopy; return in }, DropPaste, ""},
		{"a link drag pastes", func(in DropInput) DropInput { in.Intent = IntentLink; return in }, DropPaste, ""},
		{"a drag that would link pastes", func(in DropInput) DropInput { in.MovesAsLink, in.Occupied = true, true; return in }, DropPaste, ""},
		{"a refusal snaps back with its reason", func(in DropInput) DropInput {
			in.PasteRefusal = "Notes is not on the shell's node"
			return in
		}, DropRejected, "Notes is not on the shell's node"},
		{"the trashcan still deletes", func(in DropInput) DropInput { in.OverDelete = true; return in }, DropDelete, ""},
		{"a template still creates", func(in DropInput) DropInput { in.IsTemplate = true; return in }, DropCreateTemplate, ""},
		{"a bare click still navigates", func(in DropInput) DropInput { in.Started = false; return in }, DropNavigate, ""},
	} {
		in := c.in(onto)
		if got, why := DecideDrop(in), WhyRejected(in); got != c.want || why != c.why {
			t.Errorf("%s: %v %q, want %v %q", c.name, got, why, c.want, c.why)
		}
	}
}
