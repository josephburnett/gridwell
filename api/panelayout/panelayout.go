// Package panelayout is the persisted pane-layout format: a pane tile's
// blob wire shape. It is contract, not client machinery — the client encodes
// and decodes full trees from these structs, and the server derives the one
// thing it needs from a blob here (TextFocusIDs), so there is no second
// decoder to drift.
//
// Versioning: bump Version only with a new DTO type and a decoder that
// still accepts every older version. A blob written by a newer Gridwell is
// ErrLayoutVersion, and callers must treat that pane tile read-only rather
// than overwrite a newer format with a downgrade.
package panelayout

import (
	"encoding/json"
	"errors"
	"fmt"
)

// LayoutMediaType tags the layout blob in the store.
const LayoutMediaType = "application/vnd.gridwell.pane-layout+json"

// Version is the current wire version.
const Version = 1

// ErrLayoutVersion reports a layout blob written by a newer Gridwell than
// this one.
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

// LayoutSplit is an interior split.
type LayoutSplit struct {
	Dir   string     `json:"dir"`
	Ratio float64    `json:"ratio"`
	A     LayoutNode `json:"a"`
	B     LayoutNode `json:"b"`
}

// LayoutFrame is one level of a leaf's place: the doorway it came through
// and, where the frame owns it, the grid that doorway opened. A crossing into
// another namespace — a link tile, a mount, a plugin swatch — is the frame
// that carries GridID; an ordinary well frame carries only Door and its grid
// is derived from the row. Content marks a frame whose place is the door tile
// itself. No viewport: the viewports a pane would ascend onto are
// session-only, and only the leaf's current one is persisted (Cx/Cy/Zoom).
type LayoutFrame struct {
	Door    string `json:"d,omitempty"`
	GridID  string `json:"g,omitempty"`
	Content bool   `json:"c,omitempty"`
}

// LayoutPane is a leaf's persisted place: the whole frame stack in Place,
// root first, plus the leaf's viewport and content-descent state. All ids are
// in the owning node's namespace frame.
//
// Anchor, Path and TextFocus are the same place projected onto its innermost
// namespace level — the only shape a Gridwell older than Place can read, and
// the shape TextFocusIDs scans for referenced content tiles. Place wins
// wherever it is present, and it is written only where the projection would
// lose a level (a crossing below the top, a content frame below the top), so
// a place the projection holds in full encodes byte-identically to what
// earlier versions wrote and re-visiting a workspace still writes nothing.
type LayoutPane struct {
	ID          string        `json:"id"`
	Anchor      string        `json:"anchor,omitempty"`
	Path        []string      `json:"path,omitempty"`
	Cx          float64       `json:"cx,omitempty"`
	Cy          float64       `json:"cy,omitempty"`
	Zoom        float64       `json:"zoom,omitempty"`
	TextFocus   string        `json:"text_focus,omitempty"`
	TextMode    string        `json:"text_mode,omitempty"`
	TextScrollX float64       `json:"text_scroll_x,omitempty"`
	TextScrollY float64       `json:"text_scroll_y,omitempty"`
	TextZoom    float64       `json:"text_zoom,omitempty"`
	Place       []LayoutFrame `json:"place,omitempty"`
}

// Parse unmarshals and version-checks a layout blob.
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

// TextFocusIDs returns every leaf's TextFocus id in the blob: the content
// tiles a pane tile references. It is the ONE answer to that question on the
// server, read from both sides of the ephemeral reap — the store's boot sweep
// protects these ids, and the router reaps them when the pane tile is
// destroyed — so the two cannot disagree about what a blob references. The
// projection field is what it reads, not the Place stack, because every
// encoder writes TextFocus for a content descent and blobs older than Place
// carry nothing else.
//
// It is deliberately loose on structure: a node carrying both a pane and a
// split still yields the ids it does carry, since a reference named by the
// blob is a real reference either way, and the reap acts only on ids that
// also turn out to live on the owner's scratch grid.
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
