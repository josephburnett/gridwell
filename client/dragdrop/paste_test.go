package dragdrop

import "testing"

// A right-drop onto a paste target pastes, a link drag's too, and only a menu
// item or the early arms beat it; a left-drop, a finger's included, is refused
// and told, since a paste leaves the tile where it is (Joe, 2026-11-16); a
// refusal snaps back, told and named under the badge.
func TestDecideDropOntoAPasteTarget(t *testing.T) {
	onto := DropInput{Started: true, TileID: "u/1", HasTarget: true, PasteTarget: true, Intent: IntentCopy}
	for _, c := range []struct {
		name string
		in   func(DropInput) DropInput
		want DropAction
		why  string
		told string
	}{
		{"a right-drag pastes", func(in DropInput) DropInput { return in }, DropPaste, "", ""},
		{"a link drag pastes", func(in DropInput) DropInput { in.Intent = IntentLink; return in }, DropPaste, "", ""},
		{"a drag that would link pastes", func(in DropInput) DropInput { in.CopiesAsLink, in.Occupied = true, true; return in }, DropPaste, "", ""},
		{"a left-drag is refused and told", func(in DropInput) DropInput { in.Intent = IntentMove; return in }, DropRejected, PasteNeedsRight, PasteNeedsRight},
		{"a left-drag that would link is refused", func(in DropInput) DropInput { in.Intent, in.MovesAsLink = IntentMove, true; return in },
			DropRejected, PasteNeedsRight, PasteNeedsRight},
		{"a finger's drag is a left-drag", func(in DropInput) DropInput { in.Intent, in.Touch = IntentMove, true; return in },
			DropRejected, PasteNeedsRight, PasteNeedsRight},
		{"a refusal snaps back and tells its reason", func(in DropInput) DropInput {
			in.PasteRefusal = "Notes is not on the shell's node"
			return in
		}, DropRejected, "Notes is not on the shell's node", "Notes is not on the shell's node"},
		{"a text that takes no edits tells its reason", func(in DropInput) DropInput {
			in.PasteRefusal = "x.txt cannot be written: permission denied"
			return in
		}, DropRejected, "x.txt cannot be written: permission denied", "x.txt cannot be written: permission denied"},
		{"the trashcan still deletes", func(in DropInput) DropInput { in.Intent, in.OverDelete = IntentMove, true; return in }, DropDelete, "", ""},
		{"a menu item still creates", func(in DropInput) DropInput { in.FromMenu = true; return in }, DropFromMenu, "", ""},
		{"a bare click still navigates", func(in DropInput) DropInput { in.Started = false; return in }, DropNavigate, "", ""},
	} {
		in := c.in(onto)
		got, why := DecideDrop(in), WhyRejected(in)
		if got != c.want || why != c.why || Told(in) != c.told {
			t.Errorf("%s: %v %q told %q, want %v %q told %q", c.name, got, why, Told(in), c.want, c.why, c.told)
		}
		p := GhostPlanForDrop(got, in, 40, 40)
		if refused := got == DropRejected; refused != p.Forbidden || refused && p.Why != why {
			t.Errorf("%s: ghost %+v, want the badge naming %q", c.name, p, why)
		}
	}
}
