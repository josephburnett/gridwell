package mcpdoor

import (
	"fmt"

	"github.com/josephburnett/gridwell/client/pane"
)

// Layout is a pane tile's content as the door answers it: the panes it holds
// and the version it was read at, the claim a later write of it would make.
// Views, scrolls and the split's shape are the window's, not answered.
type Layout struct {
	Version int64        `json:"version" jsonschema:"the version the layout was read at"`
	Panes   []LayoutPane `json:"panes"`
}

// LayoutPane is one pane of a layout, in tree order.
type LayoutPane struct {
	ID      string   `json:"id"`
	Anchor  string   `json:"anchor" jsonschema:"the grid the pane's place is rooted at"`
	Path    []string `json:"path,omitempty" jsonschema:"the doorway tiles from the anchor down to the pane's grid"`
	Grid    string   `json:"grid,omitempty" jsonschema:"the grid the pane shows, when the layout names it: its anchor, with no path"`
	Content string   `json:"content,omitempty" jsonschema:"the content tile the pane is inside, when it is inside one"`
	Focused bool     `json:"focused,omitempty" jsonschema:"the pane the pane tile reopens on"`
}

// layoutOf decodes the layout of pane tile tileID with the client's own
// decoder (pane.DecodeLayout), spelling its ids as the tile's are: the blob
// holds them in the owning node's frame.
func layoutOf(tileID string, data []byte, version int64) (*Layout, error) {
	prefix := pane.ChainPrefix(tileID)
	tree, err := pane.DecodeLayout(data, func(id string) string { return prefix + id }, "")
	if err != nil {
		return nil, fmt.Errorf("refused: pane tile %s's layout could not be read: %v", tileID, err)
	}
	out := &Layout{Version: version, Panes: []LayoutPane{}}
	tree.Walk(func(p *pane.Pane) {
		lp := LayoutPane{ID: p.ID, Anchor: p.Anchor(), Path: p.Path(), Content: p.ContentID(), Focused: p.ID == tree.Focus}
		if len(lp.Path) == 0 {
			lp.Grid = lp.Anchor
		}
		out.Panes = append(out.Panes, lp)
	})
	return out, nil
}
