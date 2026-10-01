// The URL codec: one of the two encodings of a pane's frame stack, the other
// being the layout blob in wire.go. It is not a second place model.
//
//	/                                home, default viewport
//	/3/4/5                           descended through tiles 3, 4, 5 (home)
//	/k3x9m2q/1/~L2hvbWUvam9l         plugin k3x9m2q, grid 1, key-form tile
//	/ssh4321/remote9/1/4/7           chained remote anchor, then tiles
//	/3/4/5?x=12.5&y=-3&z=1.5         grid leaf, viewport center + zoom
//	/3/4/5/9?c=24&r=10               content leaf in text mode, cursor
//
// Leading namespace segments are the anchor's chain, the first tile segment
// is the anchor grid id, and the rest are tile ids in descent order.
// rpc.IsTileSegment decides which is which, shared with the router so the two
// cannot disagree. c and r mean text mode with the cursor there; their absence
// means rendered mode, so `?c=0&r=0` is emitted rather than stripped.
package pane

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/josephburnett/gridwell/api/rpc"
)

// URLState is the parsed, or about-to-be-encoded, URL state.
type URLState struct {
	// Anchor is the qualified grid id the pane sits inside; "" means home.
	Anchor string
	// TileIDs is the descent path as bare segments. Its last id may be a
	// content tile, resolved after DecodeURL.
	TileIDs []string
	// Viewport, when the leaf is a grid. Only non-defaults are emitted.
	X, Y, Zoom float64
	// CursorMode is a content leaf in text mode with a cursor to preserve.
	CursorMode bool
	Col, Row   int

	// Workspace is the innermost pane tile the user is inside. When set it is
	// the whole place: the interior is the layout blob's.
	Workspace string
}

// URLDefaultZoom is the implicit zoom when z is absent.
const URLDefaultZoom = 1.0

// URLBootView is URLBootViewport's answer. SetZoom is separate because a URL
// can carry a pan without a zoom.
type URLBootView struct {
	Apply   bool
	Cx, Cy  float64
	SetZoom bool
	Zoom    float64
}

// URLBootViewport resolves the root pane's framing when the app opens with no
// descent path: the URL's viewport, else the stored root view, else nothing.
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

// URLStateOf projects a pane's place into the URL DTO. home encodes as an
// empty anchor, so "/" stays home's URL.
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

// EncodeURL renders s into a path and query. Defaults are stripped, so a fresh
// pane at root produces just "/".
func EncodeURL(s URLState) string {
	if s.Workspace != "" {
		q := url.Values{}
		q.Set("w", s.Workspace)
		return "/?" + q.Encode()
	}
	var path strings.Builder
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
			path.WriteString(rpc.LocalOf(id))
		}
	}

	q := url.Values{}
	if s.CursorMode {
		// Always emitted, so presence is detectable at zero.
		q.Set("c", strconv.Itoa(s.Col))
		q.Set("r", strconv.Itoa(s.Row))
	} else {
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

// DecodeURL parses a path and query back into a URLState. The leaf type needs
// the cache, so it is not resolved here.
func DecodeURL(raw string) (URLState, error) {
	pathPart := raw
	queryPart := ""
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		pathPart, queryPart = raw[:i], raw[i+1:]
	}

	var s URLState
	if pathPart == "" || pathPart == "/" {
		s.TileIDs = nil
	} else {
		if !strings.HasPrefix(pathPart, "/") {
			return URLState{}, errors.New("path does not start with /")
		}
		var segs []string
		for seg := range strings.SplitSeq(pathPart[1:], "/") {
			if seg != "" { // tolerate trailing/doubled slashes
				segs = append(segs, seg)
			}
		}
		first := 0
		for first < len(segs) && !rpc.IsTileSegment(segs[first]) {
			first++
		}
		var tileSegs []string
		switch {
		case first == 0:
			tileSegs = segs
		case first == len(segs):
			return URLState{}, errors.New("namespace segments with no grid id")
		default:
			s.Anchor = strings.Join(segs[:first+1], "/")
			tileSegs = segs[first+1:]
		}
		ids := make([]string, 0, len(tileSegs))
		for _, seg := range tileSegs {
			if !rpc.IsTileSegment(seg) {
				return URLState{}, errors.New("descent segment " + strconv.Quote(seg) + " is not a tile id")
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
	// The `?a=` form is decoded for old bookmarks and never emitted; the path
	// form wins when both exist.
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

// URLPlace is the structural location a URL names, everything except the
// framing. Two URLs with equal Places differ only by framing.
type URLPlace struct {
	PaneID    string
	Workspace string
	Anchor    string
	Path      string
}

func URLPlaceOf(paneID string, s URLState) URLPlace {
	return URLPlace{
		PaneID:    paneID,
		Workspace: s.Workspace,
		Anchor:    s.Anchor,
		Path:      strings.Join(s.TileIDs, "/"),
	}
}

// SameURLPlace ignores pane identity: a focus switch changes PaneID but not
// where either pane is.
func SameURLPlace(a, b URLPlace) bool {
	return a.Workspace == b.Workspace && a.Anchor == b.Anchor && a.Path == b.Path
}

// URLPushesEntry is the one owner of whether a navigation deserves a browser
// history entry, so back and forward traverse descents and ascents, never
// pans or focus switches. A pane-tile boundary pushes whatever the pane ids,
// since it swaps the whole tree.
func URLPushesEntry(prev, next URLPlace, seen bool) bool {
	if !seen || SameURLPlace(prev, next) {
		return false
	}
	if next.Workspace != prev.Workspace {
		return true
	}
	return next.PaneID == prev.PaneID
}

// urlTrimFloat strips trailing zeros so the URL bar shows 0.5 rather than
// 0.50.
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
