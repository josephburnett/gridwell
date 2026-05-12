// Package url is the encoder/decoder for Gridwell's client URL state.
//
// URL shape:
//
//	/                                root grid, default viewport
//	/3/4/5                           descended through tiles 3, 4, 5
//	/3/4/5?x=12.5&y=-3&z=1.5         grid leaf, viewport center + zoom
//	/3/4/5/9                         file leaf (rendered mode)
//	/3/4/5/9?c=24&r=10               file leaf in text mode, cursor
//
// The path segments are tile row ids in descent order from the user's
// root grid. The trailing id may be a well-tile or a file-tile; the
// caller resolves which by walking the ids against the cache after a
// successful Decode.
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
	"strconv"
	"strings"
)

// State is the parsed/about-to-be-encoded URL state.
type State struct {
	// TileIDs is the descent path of tile row ids. Empty means "root
	// grid". The trailing id may be a file-tile (resolved post-Decode).
	TileIDs []int64

	// Viewport — set when the leaf is a grid (or a file in the floating
	// view, eventually). Encoder only emits these if at least one
	// differs from its default (0, 0, 1).
	X, Y, Zoom float64

	// CursorMode is true when the leaf is a file in text mode with a
	// cursor position to preserve. Encoded as `?c=Col&r=Row`.
	CursorMode bool
	Col, Row   int
}

// DefaultZoom is the implicit zoom value when `z` is absent.
const DefaultZoom = 1.0

// Encode renders s into a path+query string suitable for
// history.replaceState. Always begins with '/'. The query is omitted
// entirely when no params are set; defaults are stripped so a fresh
// pane at root produces just "/".
func Encode(s State) string {
	var path strings.Builder
	if len(s.TileIDs) == 0 {
		path.WriteByte('/')
	} else {
		for _, id := range s.TileIDs {
			path.WriteByte('/')
			path.WriteString(strconv.FormatInt(id, 10))
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

// Decode parses a path+query string back into a State. Recognizes the
// `/g/<id>/<id>...` form; `/` (or empty) decodes to a root state.
// Anything else is rejected.
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
		segs := strings.Split(pathPart[1:], "/")
		ids := make([]int64, 0, len(segs))
		for _, seg := range segs {
			if seg == "" {
				continue // tolerate trailing slash
			}
			id, err := strconv.ParseInt(seg, 10, 64)
			if err != nil {
				return State{}, err
			}
			ids = append(ids, id)
		}
		s.TileIDs = ids
	}

	if queryPart == "" {
		return s, nil
	}
	q, err := url.ParseQuery(queryPart)
	if err != nil {
		return s, err
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
