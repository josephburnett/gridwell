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
	"net/url"
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
// tile by id, anchored at `origin` (e.g., "http://localhost:8080") so
// the link resolves when the doc is rendered outside Gridwell. An empty
// origin produces a same-origin relative href ("/N").
func HrefForTile(origin string, tileID int64) string {
	leaf := "/" + strconv.FormatInt(tileID, 10)
	if origin == "" {
		return leaf
	}
	return strings.TrimRight(origin, "/") + leaf
}

// LeafTileIDFromHref parses a markdown link href and returns the leaf
// tile id if it looks like a Gridwell descent path. Accepts:
//
//   - same-origin relative paths: "/5", "/3/4/5"
//   - absolute URLs: "http://localhost:8080/5", "https://host/3/4/5"
//
// Returns 0 for anything else (external links with non-numeric paths,
// anchors, malformed input). Origin is not validated — any URL whose
// path is a chain of positive integers is treated as a tile link.
// Cross-origin "false positives" are rare in practice and degrade
// gracefully: if no tile with that id exists the embed renders as
// "missing".
func LeafTileIDFromHref(href string) int64 {
	href = strings.TrimSpace(href)
	if href == "" {
		return 0
	}
	// Strip scheme + authority if present so we work with the path.
	if u, err := url.Parse(href); err == nil && u.Scheme != "" {
		href = u.Path
	}
	if !strings.HasPrefix(href, "/") {
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

// Markdown returns the markdown plain-link string for an embed pointing
// at the given tile, anchored at `origin`. Inside Gridwell the
// rendered-mode renderer intercepts hrefs whose path looks like a
// descent chain and paints a preview embed; outside Gridwell the link
// renders as a normal hyperlink with the alt text as its label.
//
// The plain `[alt](href)` form replaces the older `[![alt](src)](href)`
// image-in-link: the only place the image was ever fetched was an
// external viewer (e.g. VS Code) with a local Gridwell server running,
// and there it broke without an absolute URL anyway. Plain link with
// absolute origin degrades cleanly: working clickable link everywhere,
// with a preview embed reserved for the inside-Gridwell view.
func Markdown(origin string, tileID int64, alt string) string {
	return fmt.Sprintf("[%s](%s)", alt, HrefForTile(origin, tileID))
}

// DefaultAlt returns the fallback alt text used when a tile has no
// stored alt yet (a freshly-created text tile, a never-visited URL).
// Should be replaced by the per-tile stored alt at insert time when
// available.
func DefaultAlt(kind string, tileID int64) string {
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

// TextareaSyncInput is the snapshot a wasm caller hands to
// DecideTextareaSync: which tile the focus is on, which tile the
// singleton textarea is currently bound to (from the App's tracking
// field), what the textarea is showing right now, and whether the
// focused tile's blob is cached and what its content is.
type TextareaSyncInput struct {
	FocusedTileID int64
	LastTileID    int64
	CurrentValue  string
	BlobCached    bool
	BlobContent   string
}

// TextareaSyncDecision is what to do to keep the textarea coherent with
// the current focused tile. Apply by, in order:
//
//  1. If SetValue is true, write Value into the textarea.
//  2. Always store NewLastTileID into the App's tracking field — even
//     when SetValue is false, so a delayed blob fetch's second pass
//     sees the correct "same tile" state.
type TextareaSyncDecision struct {
	SetValue      bool
	Value         string
	NewLastTileID int64
}

// DecideTextareaSync drives the textarea singleton's value across focus
// shifts and async blob fetches. The rules:
//
//   - Different tile than last bound: clear immediately so the previous
//     tile's buffer doesn't leak (this is the bug where "new text tile
//     has the last edited tile's content by default"). If the blob is
//     already cached, seed with it in the same step so the user doesn't
//     see a flash of empty.
//   - Same tile, textarea empty: the value was cleared by a mode toggle
//     or by a pending blob fetch; seed when the blob arrives.
//   - Same tile, textarea non-empty: in-progress typing — preserve.
//
// LastTileID always advances to FocusedTileID so the blob-fetch
// onComplete sees the "same tile, value-driven" branch rather than
// re-clearing the user's freshly-typed content.
func DecideTextareaSync(in TextareaSyncInput) TextareaSyncDecision {
	if in.LastTileID != in.FocusedTileID {
		val := ""
		if in.BlobCached {
			val = in.BlobContent
		}
		return TextareaSyncDecision{
			SetValue:      true,
			Value:         val,
			NewLastTileID: in.FocusedTileID,
		}
	}
	if in.CurrentValue == "" && in.BlobCached {
		return TextareaSyncDecision{
			SetValue:      true,
			Value:         in.BlobContent,
			NewLastTileID: in.FocusedTileID,
		}
	}
	return TextareaSyncDecision{
		SetValue:      false,
		NewLastTileID: in.FocusedTileID,
	}
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
