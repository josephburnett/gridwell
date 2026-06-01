// Package embed holds the pure logic for Gridwell's tile-embed feature:
// classifying a pane as a doc drop target, parsing embed hrefs back to
// tile ids, building the markdown for an inserted embed, and locating
// the byte offset of a drop point inside a doc's source.
//
// The wasm client (//go:build js && wasm) is just glue: it gathers the
// inputs from DOM / canvas state, dispatches to the functions here, and
// applies the result via DOM / RPC side effects. Keeping the decision
// logic out of the wasm file means it's natively `go test`-able — which
// is the only way bugs like "raw vs rendered drop target is inverted"
// or "left-drag over a doc shows the wrong cursor" get caught before
// they reach the browser.
package embed

import (
	"fmt"
	"strconv"
	"strings"

	gwurl "github.com/josephburnett/gridwell/client/url"
)

// TextMode mirrors rpc.TextModeText / rpc.TextModeRendered. The embed
// package can't import rpc (cyclic), so it accepts the bare string.
const (
	ModeRaw      = "text"
	ModeRendered = "rendered"
)

// DocTarget classifies what a drag should do when the cursor is over a
// given text-descent pane. The wasm side maps it to ghost styling, the
// canvas cursor, and the drop commit.
type DocTarget int

const (
	// DocTargetNone — not a doc context. Fall through to grid drop rules.
	DocTargetNone DocTarget = iota
	// DocTargetRaw — raw-mode text descent. Drop inserts a markdown
	// reference; both buttons are accepted.
	DocTargetRaw
	// DocTargetRendered — rendered-mode text descent. Read-only;
	// canvas cursor flips to "not allowed".
	DocTargetRendered
)

// PaneState is the subset of pane fields ClassifyDocTarget needs. The
// wasm caller fills this in from its *pane.Pane and the URL-descent
// predicate.
type PaneState struct {
	HasTextFocus bool
	IsURLDescent bool
	TextMode     string
	Inside       bool // cursor inside the file inner-box rect
}

// ClassifyDocTarget tells the drag system how to treat a cursor over a
// candidate doc pane. URL descents and clicks outside the file inner
// box are never doc targets; raw-mode text descents accept drops;
// rendered-mode text descents reject them.
func ClassifyDocTarget(s PaneState) DocTarget {
	if !s.HasTextFocus || s.IsURLDescent || !s.Inside {
		return DocTargetNone
	}
	if s.TextMode == ModeRaw {
		return DocTargetRaw
	}
	return DocTargetRendered
}

// HrefForTile builds the markdown link href for an embed pointing at a
// tile by id. v1 form: a leaf reference "/N". The rendered-mode click
// handler resolves it via the same descent-path scheme used in pane
// URLs.
func HrefForTile(tileID int64) string {
	return "/" + strconv.FormatInt(tileID, 10)
}

// LeafTileIDFromHref parses a markdown link href as a Gridwell descent
// path and returns the leaf tile id. Returns 0 for hrefs that don't
// match the scheme (external URLs, anchors, malformed paths). Hrefs
// that aren't a single-slash form fall back to scanning the path
// segments for the rightmost numeric value, so a tile moved between
// grids after a link was inserted is still resolvable by id.
func LeafTileIDFromHref(href string) int64 {
	href = strings.TrimSpace(href)
	if href == "" || !strings.HasPrefix(href, "/") {
		return 0
	}
	st, err := gwurl.Decode(href)
	if err == nil && len(st.TileIDs) > 0 {
		return st.TileIDs[len(st.TileIDs)-1]
	}
	for _, seg := range reversed(strings.Split(strings.TrimPrefix(href, "/"), "/")) {
		if id, err := strconv.ParseInt(seg, 10, 64); err == nil && id > 0 {
			return id
		}
	}
	return 0
}

func reversed(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[len(xs)-1-i] = x
	}
	return out
}

// Dimensions returns the rendered-pixel size for an embed of a tile
// with the given cell footprint. cellPx is the doc's embed-zoom cell
// size in pixels. defaultW/H are the inline fallback for the cellsW=0
// or cellsH=0 case (degenerate tiles, shouldn't happen in practice).
func Dimensions(cellsW, cellsH int64, cellPx, defaultW, defaultH int) (int, int) {
	w := int(cellsW) * cellPx
	h := int(cellsH) * cellPx
	if w <= 0 {
		w = defaultW
	}
	if h <= 0 {
		h = defaultH
	}
	return w, h
}

// Markdown returns the full markdown image-in-link string for an embed
// of the given tile at the given pixel dimensions. The src URL points
// at the /preview/tile/N server endpoint; the link href is the leaf
// descent path. The alt text is informational (shown in external
// renderers on broken images).
func Markdown(tileID int64, w, h int, alt string) string {
	return fmt.Sprintf("[![%s](/preview/tile/%d?w=%d&h=%d)](/%d)", alt, tileID, w, h, tileID)
}

// Alt returns a human-readable alt text for an embed referencing a
// tile of the given kind and id.
func Alt(kind string, tileID int64) string {
	return fmt.Sprintf("%s tile %d", kind, tileID)
}

// Insert places `link` into `src` at byte offset `off`, padding with
// a single space on either side when the surrounding character isn't
// already whitespace. This keeps the markdown parser seeing the link
// as its own token even when the drop landed mid-word.
//
// off is clamped to [0, len(src)]; out-of-range values don't panic.
func Insert(src, link string, off int) string {
	if off < 0 {
		off = 0
	}
	if off > len(src) {
		off = len(src)
	}
	pre := ""
	if off > 0 && !isWS(src[off-1]) {
		pre = " "
	}
	post := ""
	if off < len(src) && !isWS(src[off]) {
		post = " "
	}
	return src[:off] + pre + link + post + src[off:]
}

func isWS(b byte) bool { return b == ' ' || b == '\t' || b == '\n' }

// LineEndOffset returns the byte offset of the end of the row-th line
// in src (0-indexed). Returns len(src) when row is past the last line.
// Used to convert a vertical drop coordinate to an insertion point.
func LineEndOffset(src string, row int) int {
	if row < 0 {
		row = 0
	}
	pos := 0
	for range row {
		nl := strings.IndexByte(src[pos:], '\n')
		if nl < 0 {
			return len(src)
		}
		pos += nl + 1
	}
	nl := strings.IndexByte(src[pos:], '\n')
	if nl < 0 {
		return len(src)
	}
	return pos + nl
}

// RowAt returns the line index (0-based) for a screen-relative y
// coordinate inside a text pane. innerY is the pane's inner-box top
// in the same coordinate space as sy; scrollY is the doc's current
// vertical scroll offset; lineHeight is the rendered line height. The
// row is clamped to >= 0.
func RowAt(innerY, sy, scrollY, lineHeight float64) int {
	if lineHeight <= 0 {
		return 0
	}
	dy := sy - innerY + scrollY
	return max(int(dy/lineHeight), 0)
}
