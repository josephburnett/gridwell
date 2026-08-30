// The URL codec: one of the two ENCODINGS of a pane's frame stack (the
// other is the layout blob, wire.go). It is not a second place model — a
// URL is projected from a Stack (URLStateOf) and decoded back into one
// (StackAt, after the id walk the client does against its cache).
//
// URL shape (the anchor is part of the path — a plugin id is just another
// segment):
//
//	/                                home, default viewport
//	/3/4/5                           descended through tiles 3, 4, 5 (home)
//	/k3x9m2q/1/3/4                   plugin k3x9m2q, grid 1, tiles 3, 4
//	/ssh4321/remote9/1/4/7           chained remote anchor, then tiles
//	/3/4/5?x=12.5&y=-3&z=1.5         grid leaf, viewport center + zoom
//	/3/4/5/9                         file leaf (rendered mode)
//	/3/4/5/9?c=24&r=10               file leaf in text mode, cursor
//
// The grammar has one rule: leading non-numeric segments are the anchor's
// namespace chain (plugin and node ids, guaranteed non-numeric by
// idshape.NewShortID), the first numeric segment is the anchor grid id, and
// every following segment is a tile row id in descent order. No leading
// non-numeric segment means the home anchor: "/" is the node's home grid, the
// one the handshake names. The trailing tile id may be a well tile or a
// content tile; the caller resolves which by walking the ids against the
// cache after a successful DecodeURL. The `?a=<anchor>` query form is still
// decoded, for old bookmarks, but never emitted.
//
// Presence of `c`/`r` (column and row, 0-indexed) means the content tile is
// in text mode with the cursor at that position. Absence means rendered mode.
// Defaults (`x=0`, `y=0`, `z=1`, and `c=0`, `r=0` in text mode) are still
// emitted so the URL is unambiguous: `?c=0&r=0` says "text mode, cursor at
// origin", which differs from "rendered mode", which has no query at all.
package pane

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/josephburnett/gridwell/api/rpc"
)

// URLState is the parsed/about-to-be-encoded URL state.
type URLState struct {
	// Anchor is the qualified grid id the pane currently sits inside
	// ("<plugin_id>/<grid>", chains for remote grids). Empty means home, the
	// node's own grid, whose URL is "/". It is encoded as leading path
	// segments; TileIDs are the well descents within that namespace,
	// relative to Anchor.
	Anchor string

	// TileIDs is the descent path of tile row ids. Empty means the anchor
	// grid, or with no anchor, the start screen. The trailing id may be a
	// content tile, resolved after DecodeURL. Ids are bare decimal strings
	// ("42"); the client qualifies them with the anchor's namespace.
	TileIDs []string

	// Viewport, set when the leaf is a grid. The encoder emits these only
	// when at least one differs from its default (0, 0, 1).
	X, Y, Zoom float64

	// CursorMode is true when the leaf is a content tile in text mode with a
	// cursor position to preserve. Encoded as `?c=Col&r=Row`.
	CursorMode bool
	Col, Row   int

	// Workspace is the qualified tile id of the pane tile the user is inside
	// (`?w=`). When set it is the whole place: the interior — every pane's
	// anchor, path, and viewport — is server-owned in the layout blob, so no
	// path, anchor, or viewport is encoded alongside it. Nesting is
	// session-only, so only the innermost pane tile rides the URL, and
	// ascending after a reload falls back to the pane tile's containing
	// grid.
	Workspace string
}

// URLDefaultZoom is the implicit zoom value when `z` is absent.
const URLDefaultZoom = 1.0

// URLBootView is how the root pane should be framed at boot — the result of
// URLBootViewport. Apply is false when nothing should change (keep the
// bootstrap default). SetZoom distinguishes "write this zoom" from "leave
// the pane's current zoom" (a URL can carry an X/Y pan without a zoom).
type URLBootView struct {
	Apply   bool
	Cx, Cy  float64
	SetZoom bool
	Zoom    float64
}

// URLBootViewport resolves the root pane's framing when the app opens with no
// descent path. Getting the precedence wrong silently re-frames a pane the
// user did not touch:
//
//   - URL viewport present (any of urlX/urlY/urlZoom non-zero): it wins.
//     Cx/Cy always apply; Zoom applies only when urlZoom>0 (a pan-only URL
//     keeps the pane's existing zoom).
//   - else the stored root view, if it has a positive zoom: apply all three.
//   - else: nothing — keep the bootstrap-supplied default (Apply=false).
func URLBootViewport(urlX, urlY, urlZoom, rootCx, rootCy, rootZoom float64) URLBootView {
	if urlX != 0 || urlY != 0 || urlZoom != 0 {
		v := URLBootView{Apply: true, Cx: urlX, Cy: urlY}
		if urlZoom > 0 {
			v.SetZoom = true
			v.Zoom = urlZoom
		}
		return v
	}
	if rootZoom > 0 {
		return URLBootView{Apply: true, Cx: rootCx, Cy: rootCy, SetZoom: true, Zoom: rootZoom}
	}
	return URLBootView{}
}

// URLStateOf projects a pane's place into the URL DTO — the one encode half.
// home is the home grid id, which encodes as an empty anchor so "/" stays
// home's URL. A content descent rides as the trailing tile id, with its
// cursor when it is in raw-text mode.
func URLStateOf(s *Stack, home string, isText bool, col, row int) URLState {
	var st URLState
	anchor, path := s.AnchorPathAt(s.Depth() - 1)
	st.TileIDs = append([]string(nil), path...)
	if id := s.ContentID(); id != "" {
		st.TileIDs = append(st.TileIDs, id)
		if isText {
			st.CursorMode = true
			st.Col, st.Row = col, row
		}
	} else {
		st.X, st.Y, st.Zoom = s.Cx, s.Cy, s.Zoom
	}
	if anchor != home {
		st.Anchor = anchor
	}
	return st
}

// EncodeURL renders s into a path+query string suitable for
// history.replaceState. Always begins with '/'. The query is omitted
// entirely when no params are set; defaults are stripped so a fresh
// pane at root produces just "/".
func EncodeURL(s URLState) string {
	// Inside a pane tile, that tile is the place; nothing else rides.
	if s.Workspace != "" {
		q := url.Values{}
		q.Set("w", s.Workspace)
		return "/?" + q.Encode()
	}
	var path strings.Builder
	// The anchor is path segments: it is already a slash-joined qualified
	// grid id ("<ns>/.../<grid>"), so writing it verbatim yields exactly the
	// namespace-chain-then-grid prefix the DecodeURL grammar reads back.
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
			q.Set("x", urlTrimFloat(s.X, 2))
		}
		if s.Y != 0 {
			q.Set("y", urlTrimFloat(s.Y, 2))
		}
		if s.Zoom != 0 && s.Zoom != URLDefaultZoom {
			q.Set("z", urlTrimFloat(s.Zoom, 3))
		}
	}
	encoded := q.Encode()
	if encoded == "" {
		return path.String()
	}
	return path.String() + "?" + encoded
}

// DecodeURL parses a path+query string back into a URLState. The grammar (see the
// package comment): leading non-numeric segments are the anchor's namespace
// chain, the first numeric segment is the anchor grid id, the rest are tile
// ids; no non-numeric prefix means the home anchor. `/` (or empty) decodes to
// a root state. Anything else is rejected.
//
// The leaf type (well or content tile) is not resolved here; that requires
// walking the cache. The CursorMode flag and the `c`/`r` values are set when
// the URL has them, whatever the leaf type, and the caller decides what to do
// when a grid leaf carries a c/r query.
func DecodeURL(raw string) (URLState, error) {
	// Split off query.
	pathPart := raw
	queryPart := ""
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		pathPart, queryPart = raw[:i], raw[i+1:]
	}

	var s URLState
	if pathPart == "" || pathPart == "/" {
		// Root.
		s.TileIDs = nil
	} else {
		if !strings.HasPrefix(pathPart, "/") {
			// Unknown URL shape; treat as root.
			return URLState{}, errors.New("path does not start with /")
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
			return URLState{}, errors.New("namespace segments with no grid id")
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
				return URLState{}, err
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
	// The `?a=<qualified grid>` form is still decoded so old bookmarks
	// resolve, and never emitted. The path form wins when both exist.
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

// URLPlace identifies the structural location a URL names — everything except
// framing (viewport, cursor): which pane, which pane tile, which anchor,
// which descent path. Two URLs with equal Places differ only by framing.
type URLPlace struct {
	PaneID    string
	Workspace string
	Anchor    string
	Path      string
}

// URLPlaceOf derives the URLPlace a URLState occupies for pane paneID.
func URLPlaceOf(paneID string, s URLState) URLPlace {
	return URLPlace{
		PaneID:    paneID,
		Workspace: s.Workspace,
		Anchor:    s.Anchor,
		Path:      strings.Join(s.TileIDs, "/"),
	}
}

// SameURLPlace reports whether two Places name the same structural location
// (pane identity aside — a focus switch changes PaneID but not where either
// pane is).
func SameURLPlace(a, b URLPlace) bool {
	return a.Workspace == b.Workspace && a.Anchor == b.Anchor && a.Path == b.Path
}

// URLPushesEntry decides pushState against replaceState for the URL writer.
// It is the one owner of "does this navigation deserve a browser history
// entry", so back and forward traverse descents and ascents, never pans:
//
//   - a framing-only change (same place) replaces — panning around is not
//     navigation, and back must never undo a pan;
//   - a place change in the same pane pushes — descents, ascents, and text
//     descents all move the pane somewhere else;
//   - a pane-tile boundary pushes regardless of pane identity: entering or
//     leaving one swaps the whole tree, pane ids included, and that is
//     exactly the navigation back should traverse;
//   - a pane focus switch (different pane, same tree) replaces — the user
//     did not go anywhere, the URL just tracks a different pane now;
//   - the first write after boot replaces (seen=false) — boot restores a
//     place, it does not navigate to one.
func URLPushesEntry(prev, next URLPlace, seen bool) bool {
	if !seen || SameURLPlace(prev, next) {
		return false
	}
	if next.Workspace != prev.Workspace {
		return true
	}
	return next.PaneID == prev.PaneID
}

// urlTrimFloat formats a float with `prec` decimals, then strips trailing
// zeros and a trailing decimal point so the URL bar isn't cluttered
// with "0.50" / "1.000".
func urlTrimFloat(x float64, prec int) string {
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
