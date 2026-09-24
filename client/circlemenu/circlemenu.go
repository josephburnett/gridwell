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
	// MenuPlus is the + menu's own rows, PlusItems, drawn by whichever
	// renderer caps.ChoiceMenu names.
	MenuPlus
)

// For is the one verdict. A grid's slot is the + menu, so its right-click is
// the one gesture available from every grid, and what belongs to the client
// rather than to a tile lives there.
func For(m barslot.Mode) Menu {
	switch m {
	case barslot.ModeURLBack:
		return MenuURL
	case barslot.ModePlus:
		return MenuPlus
	}
	return MenuNone
}

// Item is one row of a menu this package declares. Both renderers take the
// list and hand an ID back, so neither knows what is being chosen.
type Item struct {
	ID    string
	Label string
	State State
}

// State is what a row's check slot shows. A row that names no state has no
// slot: the native menu would otherwise draw it as a setting that is off.
type State int

const (
	StateNone State = iota
	StateOff
	StateOn
)

// stateOf is the two-state form for a row that names one.
func stateOf(on bool) State {
	if on {
		return StateOn
	}
	return StateOff
}

// DumpID is the row that writes the trace to a file. It names no state.
const DumpID = "dump"

// PlusItems is the + menu: the palettes, the one on screen checked, then the
// dump.
func PlusItems(cur theme.Theme) []Item {
	out := make([]Item, 0, len(theme.All())+1)
	for _, t := range theme.All() {
		out = append(out, Item{ID: t.String(), Label: t.Label(), State: stateOf(t == cur)})
	}
	return append(out, Item{ID: DumpID, Label: "Dump logs"})
}

// Action is what picking a row does.
type Action int

const (
	// ActionNone is an id from no row, which is a dismissal.
	ActionNone Action = iota
	// ActionTheme wears the verdict's Theme.
	ActionTheme
	// ActionDump writes the trace ring to a file.
	ActionDump
)

// Verdict is what a picked id stands for. Theme means nothing unless the
// action is ActionTheme.
type Verdict struct {
	Action Action
	Theme  theme.Theme
}

// Choose reads a row id back, so a renderer hands over a string and the shim
// acts on a verdict.
func Choose(id string) Verdict {
	if id == DumpID {
		return Verdict{Action: ActionDump}
	}
	if t, ok := theme.Parse(id); ok {
		return Verdict{Action: ActionTheme, Theme: t}
	}
	return Verdict{}
}
