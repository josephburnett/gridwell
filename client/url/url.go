// Package url is the encoder/decoder for Gridwell's client URL state.
//
// URL shape (anchor-as-path since 2026-07-25 — the owner's "plugin ids are
// just another part of the path"):
//
//	/                                home, default viewport
//	/3/4/5                           descended through tiles 3, 4, 5 (home)
//	/k3x9m2q/1/3/4                   plugin k3x9m2q, grid 1, tiles 3, 4
//	/ssh4321/remote9/1/4/7           chained remote anchor, then tiles
//	/3/4/5?x=12.5&y=-3&z=1.5         grid leaf, viewport center + zoom
//	/3/4/5/9                         file leaf (rendered mode)
//	/3/4/5/9?c=24&r=10               file leaf in text mode, cursor
//
// The grammar has one rule: LEADING NON-NUMERIC segments are the anchor's
// namespace chain (plugin/node ids — guaranteed non-numeric by config.Load
// and store.NewShortID), the FIRST numeric segment is the anchor grid id,
// and every following segment is a tile row id in descent order. No leading
// non-numeric segment means the home anchor ("/" is home — the first
// configured plugin's root grid, rpc.HomeGrid). The trailing tile id may be
// a well-tile or a file-tile; the caller resolves which by walking the ids
// against the cache after a successful Decode. The legacy `?a=<anchor>`
// query form is still DECODED (old bookmarks) but never emitted.
//
// Presence of `c`/`r` (column / row, 0-indexed) implies "file is in
// text mode with the cursor at this position". Absence means rendered
// mode. Defaults (`x=0`, `y=0`, `z=1`, `c=0`, `r=0` when in text mode)
// are still emitted so the URL is unambiguous: `?c=0&r=0` says "text
// mode, cursor at origin", which differs from "rendered mode" (no
// query at all).
package url

import (
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// State is the parsed/about-to-be-encoded URL state.
type State struct {
	// Anchor is the qualified grid id the pane currently sits inside
	// ("<plugin_id>/<grid>", chains for remote grids). Empty → HOME (the
	// first configured plugin's root grid; "/" is home's URL — NOT the node
	// grid). Encoded as leading path segments; TileIDs are the well descents
	// within that namespace, relative to Anchor.
	Anchor string

	// TileIDs is the descent path of tile row ids. Empty means "anchor
	// grid" (or, with no anchor, the start screen). The trailing id may be a
	// file-tile (resolved post-Decode). IDs are bare decimal strings (e.g.
	// "42"); the client qualifies them with the anchor's plugin UUID.
	TileIDs []string

	// Viewport — set when the leaf is a grid (or a file in the floating
	// view, eventually). Encoder only emits these if at least one
	// differs from its default (0, 0, 1).
	X, Y, Zoom float64

	// CursorMode is true when the leaf is a file in text mode with a
	// cursor position to preserve. Encoded as `?c=Col&r=Row`.
	CursorMode bool
	Col, Row   int

	// Workspace is the qualified TILE id of the pane tile the user is
	// inside (`?w=`). When set, it is the WHOLE place: the workspace's
	// interior — every pane's anchor, path, and viewport — is server-owned
	// (the layout blob), so no path/anchor/viewport is encoded alongside it
	// (one fact, one owner; the blob wins). Nesting is session-only, so
	// only the innermost workspace rides the URL; ascending after a reload
	// falls back to the pane tile's containing grid.
	Workspace string
}

// DefaultZoom is the implicit zoom value when `z` is absent.
const DefaultZoom = 1.0

// TextState builds the State for a text-tile descent: the descent path
// plus the focused text tile as the trailing id. When isTextMode is true
// the leaf is in raw-text mode and carries its cursor (col, row) so the
// position is restored on reload; in rendered mode no cursor is encoded.
// path is cloned — the caller's slice is never retained.
func TextState(path []string, textFocusTileID string, isTextMode bool, col, row int) State {
	s := State{TileIDs: append(slices.Clone(path), textFocusTileID)}
	if isTextMode {
		s.CursorMode = true
		s.Col = col
		s.Row = row
	}
	return s
}

// BootView is how the root pane should be framed at boot — the result of
// BootViewport. Apply is false when nothing should change (keep the
// bootstrap default). SetZoom distinguishes "write this zoom" from "leave
// the pane's current zoom" (a URL can carry an X/Y pan without a zoom).
type BootView struct {
	Apply   bool
	Cx, Cy  float64
	SetZoom bool
	Zoom    float64
}

// BootViewport resolves the root pane's framing when the app opens with no
// descent path, by this precedence — getting it wrong silently re-frames a
// pane the user didn't touch (a "things stay where you put them" violation):
//
//   - URL viewport present (any of urlX/urlY/urlZoom non-zero): it wins.
//     Cx/Cy always apply; Zoom applies only when urlZoom>0 (a pan-only URL
//     keeps the pane's existing zoom).
//   - else the stored root view, if it has a positive zoom: apply all three.
//   - else: nothing — keep the bootstrap-supplied default (Apply=false).
func BootViewport(urlX, urlY, urlZoom, rootCx, rootCy, rootZoom float64) BootView {
	if urlX != 0 || urlY != 0 || urlZoom != 0 {
		v := BootView{Apply: true, Cx: urlX, Cy: urlY}
		if urlZoom > 0 {
			v.SetZoom = true
			v.Zoom = urlZoom
		}
		return v
	}
	if rootZoom > 0 {
		return BootView{Apply: true, Cx: rootCx, Cy: rootCy, SetZoom: true, Zoom: rootZoom}
	}
	return BootView{}
}

// GridState builds the State for a grid (or rendered-file) descent: the
// descent path as the tile ids and the pane viewport (center + zoom).
// path is cloned.
func GridState(path []string, cx, cy, zoom float64) State {
	return State{
		TileIDs: slices.Clone(path),
		X:       cx,
		Y:       cy,
		Zoom:    zoom,
	}
}

// Encode renders s into a path+query string suitable for
// history.replaceState. Always begins with '/'. The query is omitted
// entirely when no params are set; defaults are stripped so a fresh
// pane at root produces just "/".
func Encode(s State) string {
	// Inside a workspace the pane tile IS the place; nothing else rides.
	if s.Workspace != "" {
		q := url.Values{}
		q.Set("w", s.Workspace)
		return "/?" + q.Encode()
	}
	var path strings.Builder
	// The anchor is path segments: it is already a slash-joined qualified
	// grid id ("<ns>/.../<grid>"), so writing it verbatim yields exactly the
	// namespace-chain-then-grid prefix the Decode grammar reads back.
	if s.Anchor != "" {
		path.WriteByte('/')
		path.WriteString(s.Anchor)
	}
	if len(s.TileIDs) == 0 {
		if s.Anchor == "" {
			path.WriteByte('/')
		}
	} else {
		for _, id := range s.TileIDs {
			path.WriteByte('/')
			// Strip the namespace prefix (e.g. "uuid/42" → "42") for
			// human-readable URLs; the client re-qualifies on decode.
			path.WriteString(rpc.LocalOf(id))
		}
	}

	q := url.Values{}
	if s.CursorMode {
		// Text mode: always emit c and r so presence can be detected
		// even when both are zero.
		q.Set("c", strconv.Itoa(s.Col))
		q.Set("r", strconv.Itoa(s.Row))
	} else {
		// Grid (or rendered file) leaf: only emit non-default viewport
		// values to keep the URL short.
		if s.X != 0 {
			q.Set("x", trimFloat(s.X, 2))
		}
		if s.Y != 0 {
			q.Set("y", trimFloat(s.Y, 2))
		}
		if s.Zoom != 0 && s.Zoom != DefaultZoom {
			q.Set("z", trimFloat(s.Zoom, 3))
		}
	}
	encoded := q.Encode()
	if encoded == "" {
		return path.String()
	}
	return path.String() + "?" + encoded
}

// Decode parses a path+query string back into a State. The grammar (see the
// package comment): leading non-numeric segments are the anchor's namespace
// chain, the first numeric segment is the anchor grid id, the rest are tile
// ids; no non-numeric prefix means the home anchor. `/` (or empty) decodes to
// a root state. Anything else is rejected.
//
// The leaf type (well vs file) is *not* resolved here — that requires
// walking the cache. The `CursorMode` flag and `c`/`r` values are set
// when the URL has them, regardless of leaf type; the caller decides
// what to do if a non-file leaf has a c/r query.
func Decode(raw string) (State, error) {
	// Split off query.
	pathPart := raw
	queryPart := ""
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		pathPart, queryPart = raw[:i], raw[i+1:]
	}

	var s State
	if pathPart == "" || pathPart == "/" {
		// Root.
		s.TileIDs = nil
	} else {
		if !strings.HasPrefix(pathPart, "/") {
			// Unknown URL shape; treat as root.
			return State{}, errors.New("path does not start with /")
		}
		var segs []string
		for seg := range strings.SplitSeq(pathPart[1:], "/") {
			if seg != "" { // tolerate trailing/doubled slashes
				segs = append(segs, seg)
			}
		}
		// The anchor/path boundary: skip the leading non-numeric namespace
		// segments; the first numeric segment is the anchor grid id.
		first := 0
		for first < len(segs) {
			if _, err := strconv.ParseInt(segs[first], 10, 64); err == nil {
				break
			}
			first++
		}
		var tileSegs []string
		switch {
		case first == 0:
			// No namespace prefix: a bare tile path under the home anchor.
			tileSegs = segs
		case first == len(segs):
			// Namespace segments with no grid id — not a Gridwell place
			// (this also catches arbitrary external-looking paths).
			return State{}, errors.New("namespace segments with no grid id")
		default:
			s.Anchor = strings.Join(segs[:first+1], "/")
			tileSegs = segs[first+1:]
		}
		ids := make([]string, 0, len(tileSegs))
		for _, seg := range tileSegs {
			// Every descent segment must be an integer (Gridwell URL
			// detection); returned as strings — the client qualifies them
			// with the anchor's namespace after decoding.
			if _, err := strconv.ParseInt(seg, 10, 64); err != nil {
				return State{}, err
			}
			ids = append(ids, seg)
		}
		if len(ids) > 0 {
			s.TileIDs = ids
		}
	}

	if queryPart == "" {
		return s, nil
	}
	q, err := url.ParseQuery(queryPart)
	if err != nil {
		return s, err
	}
	// Legacy anchor form (`?a=<qualified grid>`): still decoded so old
	// bookmarks resolve; never emitted. The path form wins when both exist.
	if v, ok := q["a"]; ok && s.Anchor == "" {
		s.Anchor = v[0]
	}
	if v, ok := q["w"]; ok {
		s.Workspace = v[0]
	}
	if cv, ok := q["c"]; ok {
		if rv, okR := q["r"]; okR {
			c, err1 := strconv.Atoi(cv[0])
			r, err2 := strconv.Atoi(rv[0])
			if err1 == nil && err2 == nil {
				s.CursorMode = true
				s.Col = c
				s.Row = r
			}
		}
	} else {
		if v, ok := q["x"]; ok {
			s.X, _ = strconv.ParseFloat(v[0], 64)
		}
		if v, ok := q["y"]; ok {
			s.Y, _ = strconv.ParseFloat(v[0], 64)
		}
		if v, ok := q["z"]; ok {
			s.Zoom, _ = strconv.ParseFloat(v[0], 64)
		}
	}
	return s, nil
}

// Place identifies the STRUCTURAL location a URL names — everything except
// framing (viewport, cursor): which pane, which workspace, which anchor,
// which descent path. Two URLs with equal Places differ only by framing.
type Place struct {
	PaneID    string
	Workspace string
	Anchor    string
	Path      string
}

// PlaceOf derives the Place a State occupies for pane paneID.
func PlaceOf(paneID string, s State) Place {
	return Place{
		PaneID:    paneID,
		Workspace: s.Workspace,
		Anchor:    s.Anchor,
		Path:      strings.Join(s.TileIDs, "/"),
	}
}

// SamePlace reports whether two Places name the same structural location
// (pane identity aside — a focus switch changes PaneID but not where either
// pane is).
func SamePlace(a, b Place) bool {
	return a.Workspace == b.Workspace && a.Anchor == b.Anchor && a.Path == b.Path
}

// PushesEntry decides pushState vs replaceState for the URL writer — the one
// owner of "does this navigation deserve a browser history entry" (issue
// #194: back/forward traverse descend/ascend, never pan/zoom):
//
//   - a framing-only change (same place) REPLACES — panning around is not
//     navigation, and back must never undo a pan;
//   - a place change in the SAME pane PUSHES — descend, ascend, portal, and
//     text descents all move the pane somewhere else;
//   - a workspace boundary PUSHES regardless of pane identity — entering or
//     leaving a workspace swaps the whole tree (pane ids change with it),
//     but it is exactly the kind of navigation back should traverse;
//   - a pane FOCUS switch (different pane, same workspace) REPLACES — the
//     user didn't go anywhere, the URL just tracks a different pane now;
//   - the first write after boot REPLACES (seen=false) — boot restores a
//     place, it doesn't navigate to one.
func PushesEntry(prev, next Place, seen bool) bool {
	if !seen || SamePlace(prev, next) {
		return false
	}
	if next.Workspace != prev.Workspace {
		return true
	}
	return next.PaneID == prev.PaneID
}

// trimFloat formats a float with `prec` decimals, then strips trailing
// zeros and a trailing decimal point so the URL bar isn't cluttered
// with "0.50" / "1.000".
func trimFloat(x float64, prec int) string {
	s := strconv.FormatFloat(x, 'f', prec, 64)
	if strings.ContainsRune(s, '.') {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	if s == "" {
		return "0"
	}
	return s
}
