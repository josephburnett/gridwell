// Package panelayout is the persisted pane-layout format: a pane tile's
// blob wire shape. It is contract, not client machinery — the home store
// scans blobs for the text tiles a pane tile references (the ephemeral
// reap's protection set) and the client encodes and decodes full trees
// from the same structs, so there is no second decoder to drift.
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

// LayoutPane is a leaf's persisted place: anchor, path, and viewport, plus
// the content-descent state. All ids are in the owning node's namespace
// frame.
type LayoutPane struct {
	ID          string   `json:"id"`
	Anchor      string   `json:"anchor,omitempty"`
	Path        []string `json:"path,omitempty"`
	Cx          float64  `json:"cx,omitempty"`
	Cy          float64  `json:"cy,omitempty"`
	Zoom        float64  `json:"zoom,omitempty"`
	TextFocus   string   `json:"text_focus,omitempty"`
	TextMode    string   `json:"text_mode,omitempty"`
	TextScrollX float64  `json:"text_scroll_x,omitempty"`
	TextScrollY float64  `json:"text_scroll_y,omitempty"`
	TextZoom    float64  `json:"text_zoom,omitempty"`
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
// tiles a pane tile references. The store's reap reads this to protect
// those documents without holding any client tree machinery.
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
