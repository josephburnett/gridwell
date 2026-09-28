//go:build js && wasm

package main

import (
	"slices"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
)

// mirrorState is pane.Mirrored's last answer as each mirror received it.
type mirrorState struct {
	url     []string
	urlSent bool
}

// syncMirrors runs every frame, because every change that can show or hide a
// face draws one; a frame that changes no answer says nothing.
func (a *App) syncMirrors(rects map[string]pane.Rect) {
	urls := a.urlSurfaces()
	var shown []pane.Face
	if len(urls) > 0 {
		shown = a.shownFaces(rects)
	}
	url := pane.Mirrored(urls, shown)
	if !a.mirrors.urlSent || !slices.Equal(url, a.mirrors.url) {
		a.mirrors.url, a.mirrors.urlSent = url, true
		a.bridgeSetMirrored(url)
	}
}

// shownFaces is every content tile a laid-out pane draws the face of, in the
// shape pane.Mirrored reads.
func (a *App) shownFaces(rects map[string]pane.Rect) []pane.Face {
	var out []pane.Face
	for paneID := range rects {
		p := a.tree.FindPane(paneID)
		if p == nil {
			continue
		}
		if p.ContentID() != "" {
			if t, ok := a.descendedTile(p); ok {
				out = append(out, pane.Face{PaneID: paneID, ContentID: rpc.ContentID(t)})
			}
			continue
		}
		if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
			for _, n := range g.Tiles {
				out = append(out, pane.Face{PaneID: paneID, ContentID: rpc.ContentID(n)})
			}
		}
	}
	return out
}
