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
		{"a grid's + menu offers the theme", barslot.ModePlus, MenuTheme},
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

// The theme menu offers every theme and checks exactly the one on screen, so a
// renderer never has to decide what "current" means.
func TestThemeItems(t *testing.T) {
	for _, cur := range theme.All() {
		items := ThemeItems(cur)
		if len(items) != len(theme.All()) {
			t.Fatalf("ThemeItems(%v) has %d rows, want %d", cur, len(items), len(theme.All()))
		}
		checked := 0
		for _, it := range items {
			if it.Label == "" || it.ID == "" {
				t.Errorf("ThemeItems(%v): a row is unnamed: %+v", cur, it)
			}
			if it.Checked {
				checked++
				if it.ID != cur.String() {
					t.Errorf("ThemeItems(%v) checks %q", cur, it.ID)
				}
			}
		}
		if checked != 1 {
			t.Errorf("ThemeItems(%v) checks %d rows, want 1", cur, checked)
		}
	}
}

// The id a renderer hands back is the one Parse reads, so a pick round-trips
// without a second table.
func TestThemeItemIDsParse(t *testing.T) {
	for _, it := range ThemeItems(theme.Default()) {
		got, ok := theme.Parse(it.ID)
		if !ok || got.String() != it.ID {
			t.Errorf("Parse(%q) = %v, %v", it.ID, got, ok)
		}
	}
}
