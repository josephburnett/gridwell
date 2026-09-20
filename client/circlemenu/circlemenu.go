// Package circlemenu owns what a right-click on the bottom bar's circle slot
// offers. It reads barslot.Decide's verdict, so no mode can grow a menu the
// slot does not stand for.
package circlemenu

import (
	"github.com/josephburnett/gridwell/client/barslot"
	"github.com/josephburnett/gridwell/client/theme"
)

// Menu names which menu a right-click pops, and so which renderer draws it.
type Menu int

const (
	// MenuNone is a slot with nothing to offer; the press is swallowed.
	MenuNone Menu = iota
	// MenuURL is the live url view's own context menu. Its rows are the page's
	// and are built by the host, which is why this carries none.
	MenuURL
	// MenuTheme chooses the client's palette. Its rows are ThemeItems, drawn
	// by whichever renderer caps.ChoiceMenu names.
	MenuTheme
)

// For is the one verdict. A grid's slot is the + menu, so its right-click is
// the one gesture available from every grid, and the theme lives there.
func For(m barslot.Mode) Menu {
	switch m {
	case barslot.ModeURLBack:
		return MenuURL
	case barslot.ModePlus:
		return MenuTheme
	}
	return MenuNone
}

// Item is one row of a menu this package declares. Both renderers take the
// list and hand an ID back, so neither knows what is being chosen.
type Item struct {
	ID      string
	Label   string
	Checked bool
}

// ThemeItems is the theme menu, the palette on screen checked.
func ThemeItems(cur theme.Theme) []Item {
	out := make([]Item, 0, len(theme.All()))
	for _, t := range theme.All() {
		out = append(out, Item{ID: t.String(), Label: t.Label(), Checked: t == cur})
	}
	return out
}
