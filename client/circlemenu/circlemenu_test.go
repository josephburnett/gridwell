package circlemenu

import (
	"testing"

	"github.com/josephburnett/gridwell/client/barslot"
	"github.com/josephburnett/gridwell/client/theme"
)

// One row per slot verdict, so a mode added to barslot has to say here what
// its right-click does rather than silently falling into a menu.
func TestFor(t *testing.T) {
	cases := []struct {
		name string
		mode barslot.Mode
		want Menu
	}{
		{"a grid's + menu offers the client's own rows", barslot.ModePlus, MenuPlus},
		{"a live url's back button pops the page's own menu", barslot.ModeURLBack, MenuURL},
		{"a frozen url's go-live has no menu", barslot.ModeGoLive, MenuNone},
		{"a browser host's new-tab button has no menu", barslot.ModeURLOpenTab, MenuNone},
		{"a live shell's freeze has no menu", barslot.ModeFreeze, MenuNone},
		{"an empty slot has no menu", barslot.ModeNothing, MenuNone},
	}
	for _, c := range cases {
		if got := For(c.mode); got != c.want {
			t.Errorf("%s: For(%v) = %v, want %v", c.name, c.mode, got, c.want)
		}
	}
}

// The + menu offers every theme and checks exactly the one on screen, so a
// renderer never has to decide what "current" means. The dump is a gesture
// rather than a state, so it is never the checked row.
func TestPlusItems(t *testing.T) {
	for _, cur := range theme.All() {
		items := PlusItems(cur)
		if len(items) != len(theme.All())+1 {
			t.Fatalf("PlusItems(%v) has %d rows, want the themes and the dump", cur, len(items))
		}
		if last := items[len(items)-1]; last.ID != DumpID || last.Label != "Dump logs" || last.Checked {
			t.Errorf("PlusItems(%v) ends with %+v, want an unchecked dump row", cur, last)
		}
		checked := 0
		seen := map[string]bool{}
		for _, it := range items {
			if it.Label == "" || it.ID == "" {
				t.Errorf("PlusItems(%v): a row is unnamed: %+v", cur, it)
			}
			if seen[it.ID] {
				t.Errorf("PlusItems(%v) offers %q twice, so a pick is ambiguous", cur, it.ID)
			}
			seen[it.ID] = true
			if it.Checked {
				checked++
				if it.ID != cur.String() {
					t.Errorf("PlusItems(%v) checks %q", cur, it.ID)
				}
			}
		}
		if checked != 1 {
			t.Errorf("PlusItems(%v) checks %d rows, want 1", cur, checked)
		}
	}
}

// Every row a renderer can hand back stands for something, so a row cannot be
// offered that the shim then does nothing with.
func TestChooseReadsEveryRowBack(t *testing.T) {
	for _, it := range PlusItems(theme.Default()) {
		v := Choose(it.ID)
		switch {
		case it.ID == DumpID:
			if v.Action != ActionDump {
				t.Errorf("Choose(%q) = %+v, want the dump", it.ID, v)
			}
		default:
			if v.Action != ActionTheme || v.Theme.String() != it.ID {
				t.Errorf("Choose(%q) = %+v, want that theme", it.ID, v)
			}
		}
	}
}

// A dismissal hands back no id, and an id from no row is the same thing: the
// menu changes nothing by itself.
func TestChooseOnAnIDFromNoRow(t *testing.T) {
	for _, id := range []string{"", "dark ", "Dump logs", "theme"} {
		if v := Choose(id); v.Action != ActionNone {
			t.Errorf("Choose(%q) = %+v, want nothing", id, v)
		}
	}
}
