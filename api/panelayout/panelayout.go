// Package panelayout is the wire shape of a workspace tile's persisted layout
// blob and the only decoder for it. Bump Version only with a new DTO type
// and a decoder that still accepts every older version. A blob from a newer
// Gridwell is ErrLayoutVersion, and its workspace tile goes read-only rather than
// be overwritten by a downgrade.
package panelayout

import (
	"encoding/json"
	"errors"
	"fmt"
)

// LayoutMediaType tags the layout blob in the store.
const LayoutMediaType = "application/vnd.gridwell.pane-layout+json"

const Version = 1

// ErrLayoutVersion reports a blob written by a newer Gridwell.
var ErrLayoutVersion = errors.New("pane layout: unsupported version")

// LayoutV1 is wire version 1 of a persisted pane tree.
type LayoutV1 struct {
	V      int        `json:"v"`
	Root   LayoutNode `json:"root"`
	Focus  string     `json:"focus,omitempty"`
	Zoomed string     `json:"zoomed,omitempty"`
}

// LayoutNode holds exactly one of Pane or Split.
type LayoutNode struct {
	Pane  *LayoutPane  `json:"pane,omitempty"`
	Split *LayoutSplit `json:"split,omitempty"`
}

type LayoutSplit struct {
	Dir   string     `json:"dir"`
	Ratio float64    `json:"ratio"`
	A     LayoutNode `json:"a"`
	B     LayoutNode `json:"b"`
}

// LayoutFrame is one level of a leaf's place. Only a crossing into another
// namespace carries GridID; a door frame carries Door alone and its grid is
// derived from the row. Content marks a frame whose place is the door tile
// itself.
type LayoutFrame struct {
	Door    string `json:"d,omitempty"`
	GridID  string `json:"g,omitempty"`
	Content bool   `json:"c,omitempty"`
}

// LayoutPane is a leaf's persisted place: the frame stack in Place, root
// first. Anchor, Path and TextFocus are the same place projected onto its
// innermost namespace level. Place wins where present and is written only
// where the projection would lose a level, so what the projection holds in
// full still encodes byte-identically.
// Views is the viewport each frame of the place was left at, root first, one
// per frame, null where a frame has none; absent when no frame has one. Text
// is the live scroll of a text descent on top, absent until the user moves it
// off the row's; its mode, and its scroll once left, are its tile row's
// (2026-10-16). A selection is the grid row's, never the layout's
// (2026-10-18). Retired keys, never reused, and read past: cx, cy, zoom,
// text_mode, text_scroll_x, text_scroll_y, text_zoom, sel.
type LayoutPane struct {
	ID        string        `json:"id"`
	Anchor    string        `json:"anchor,omitempty"`
	Path      []string      `json:"path,omitempty"`
	TextFocus string        `json:"text_focus,omitempty"`
	Place     []LayoutFrame `json:"place,omitempty"`
	Views     []*LayoutView `json:"views,omitempty"`
	Text      *LayoutText   `json:"text,omitempty"`
}

// LayoutText is a text descent's scroll in doc px.
type LayoutText struct {
	X float64 `json:"x,omitempty"`
	Y float64 `json:"y,omitempty"`
}

// LayoutView is one frame's viewport: a center in its grid's cells and the
// live zoom, the scale the pane drew it at, so a reopened pane draws the
// cells the size it left them whatever the window's size.
type LayoutView struct {
	Cx   float64 `json:"x"`
	Cy   float64 `json:"y"`
	Zoom float64 `json:"z"`
}

// Parse unmarshals a layout blob and rejects an unsupported Version.
func Parse(data []byte) (*LayoutV1, error) {
	var l LayoutV1
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("pane layout: %w", err)
	}
	if l.V != Version {
		return nil, fmt.Errorf("%w: v=%d", ErrLayoutVersion, l.V)
	}
	return &l, nil
}

// TextFocusIDs returns the content tiles a workspace tile references. Both sides
// of the ephemeral reap read it, the store's boot sweep and the router, so
// the two cannot disagree. It reads TextFocus because a blob older than
// Place carries nothing else.
func TextFocusIDs(data []byte) ([]string, error) {
	l, err := Parse(data)
	if err != nil {
		return nil, err
	}
	var out []string
	var walk func(n LayoutNode)
	walk = func(n LayoutNode) {
		if n.Pane != nil && n.Pane.TextFocus != "" {
			out = append(out, n.Pane.TextFocus)
		}
		if n.Split != nil {
			walk(n.Split.A)
			walk(n.Split.B)
		}
	}
	walk(l.Root)
	return out, nil
}
