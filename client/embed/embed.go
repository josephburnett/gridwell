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
	"unicode/utf8"
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
	// reference at the line under the cursor; both buttons are accepted.
	DocTargetRaw
	// DocTargetRendered — rendered-mode text descent (editable). Drop inserts
	// a markdown reference at the caret (the source offset under the cursor).
	DocTargetRendered
	// DocTargetReject — a read-only doc (a plugin's @info / file metadata).
	// Drops are not allowed; the canvas cursor flips to "not allowed".
	DocTargetReject
)

// PaneState is the subset of pane fields ClassifyDocTarget needs. The
// wasm caller fills this in from its *pane.Pane and the URL-descent
// predicate.
type PaneState struct {
	HasTextFocus bool
	IsURLDescent bool
	TextMode     string
	Inside       bool // cursor inside the file inner-box rect
	ReadOnly     bool // the tile's content can't be edited (source-backed)
}

// ClassifyDocTarget tells the drag system how to treat a cursor over a
// candidate doc pane. URL descents and clicks outside the file inner box are
// never doc targets; a read-only doc rejects drops; otherwise both raw and
// rendered text descents accept them — raw inserts at the line under the
// cursor, rendered at the caret.
func ClassifyDocTarget(s PaneState) DocTarget {
	if !s.HasTextFocus || s.IsURLDescent || !s.Inside {
		return DocTargetNone
	}
	if s.ReadOnly {
		return DocTargetReject
	}
	if s.TextMode == ModeRaw {
		return DocTargetRaw
	}
	return DocTargetRendered
}

// EmbedDescent is the plan for following an embed click into its target tile.
// OK is false when the click resolved to no real, cached tile (the wasm side
// then swallows it). Otherwise it says where to put the pane so the target
// renders, and the wasm side stashes the doc as the one-step ascent return.
//
//   - Reanchor false: the target is in the doc's current grid — focus / descend
//     it in place (Anchor / Path stay the doc's).
//   - Reanchor true: the target lives in another grid (the common case — a url
//     tile lives inside its own well, so embedding it is cross-grid). Move the
//     pane to the target's grid first (Anchor = the target tile's grid, Path =
//     its root) and then descend; this also crosses plugins, since Anchor
//     carries the plugin uuid.
type EmbedDescent struct {
	OK       bool
	Reanchor bool
	Anchor   string   // new pane anchor when Reanchor (the target's grid id)
	Path     []string // new pane path when Reanchor (nil = the target grid's root)
}

// PlanEmbedDescent decides how to follow an embed to its target. It replaces the
// old same-grid *gate* (which rejected every cross-grid embed, so "descend into
// a url tile" never worked — url tiles almost always live in a child grid) with
// a *plan* that re-anchors onto the target's grid when needed.
//
// hitTileID is the resolved embed reference ("" if the href didn't parse);
// targetGridID is the grid the found target lives in ("" if no tile was found);
// currentGridID is the grid the doc pane is in. Pure: the wasm caller resolves
// the inputs (findTileByID, gridIDForPane) and applies the plan.
func PlanEmbedDescent(hitTileID, targetGridID, currentGridID string) EmbedDescent {
	if hitTileID == "" || targetGridID == "" {
		return EmbedDescent{} // no real, cached target → don't follow
	}
	if targetGridID == currentGridID {
		return EmbedDescent{OK: true} // same grid: focus / descend in place
	}
	return EmbedDescent{OK: true, Reanchor: true, Anchor: targetGridID, Path: nil}
}

// HrefForTile builds the markdown link href for an embed pointing at a
// tile by id, anchored at `origin` (e.g., "http://localhost:8080") so
// the link resolves when the doc is rendered outside Gridwell. An empty
// origin produces a same-origin relative href.
//
// The plugin UUID is PRESERVED in the path: a qualified id "uuid/42" becomes
// "/uuid/42", a bare id "42" becomes "/42". A markdown embed link is a single
// globally-qualified id (CLAUDE.md), so carrying the uuid makes the link
// resolve to the SAME tile from any client and from a doc in any plugin —
// independent of which plugin embeds it. (It used to strip the uuid and have
// the reader re-qualify with the embedding doc's plugin; that silently
// mis-resolved every cross-plugin embed to a non-existent same-plugin id.)
func HrefForTile(origin string, tileID string) string {
	path := "/" + tileID
	if origin == "" {
		return path
	}
	return strings.TrimRight(origin, "/") + path
}

// uuidHexLen is the character length of a plugin uuid (store/newUUID: a random
// 128-bit id as lowercase hex). isPluginUUID matches exactly this shape so a
// qualified embed path "/<uuid>/<id>" is told apart from an ordinary external
// link like "/user/42" (whose first segment is not 32 hex chars) — without it,
// every numeric-tailed external link would be mis-rendered as a tile embed.
const uuidHexLen = 32

// isPluginUUID reports whether s is a plugin uuid: exactly uuidHexLen lowercase
// hex characters. Kept local because the embed package can't import the store
// or rpc packages (cyclic); the format is a stable contract (store/uuid.go).
func isPluginUUID(s string) bool {
	if len(s) != uuidHexLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// hrefSegments returns the non-empty path segments of href, or nil if href is
// not a rooted path. Absolute URLs keep their path; relative hrefs ("/3/4/5",
// "/5?x=1") parse with an empty scheme and the path we want. A parse error, or
// a path not rooted at "/", yields nil.
func hrefSegments(href string) []string {
	href = strings.TrimSpace(href)
	if href == "" {
		return nil
	}
	u, err := url.Parse(href)
	if err != nil || !strings.HasPrefix(u.Path, "/") {
		return nil
	}
	var segs []string
	for seg := range strings.SplitSeq(strings.TrimPrefix(u.Path, "/"), "/") {
		if seg != "" { // tolerate trailing/doubled slashes
			segs = append(segs, seg)
		}
	}
	return segs
}

// positiveIntLeaf returns the last segment if EVERY segment is a positive
// integer, else ("", false). A real descent ends in a numeric tile id, so an
// external link with a non-numeric segment (e.g. "/2024/recap", "/blog/01/post")
// must not be mistaken for an embed.
func positiveIntLeaf(segs []string) (string, bool) {
	if len(segs) == 0 {
		return "", false
	}
	leaf := ""
	for _, seg := range segs {
		id, err := strconv.ParseInt(seg, 10, 64)
		if err != nil || id <= 0 {
			return "", false
		}
		leaf = seg
	}
	return leaf, true
}

// parseEmbedHref interprets a markdown link href as a Gridwell tile link. Two
// shapes are accepted:
//
//   - plugin-qualified "/<uuid>/<id>[/<id>...]" → (uuid, leaf, true): the link
//     names its own plugin, so it resolves the same from any doc.
//   - bare/legacy "/<id>[/<id>...]" (all positive ints) → ("", leaf, true): a
//     pre-uuid link; the reader re-qualifies with the embedding doc's plugin.
//
// Anything else (external link, non-numeric leaf, malformed) → ("", "", false).
func parseEmbedHref(href string) (uuid, leaf string, ok bool) {
	segs := hrefSegments(href)
	if len(segs) == 0 {
		return "", "", false
	}
	if isPluginUUID(segs[0]) {
		leaf, ok := positiveIntLeaf(segs[1:])
		return segs[0], leaf, ok
	}
	leaf, ok = positiveIntLeaf(segs)
	return "", leaf, ok
}

// LeafTileIDFromHref returns the leaf (last) tile id of a Gridwell tile-link
// href, or "" if href isn't a tile link. Recognizes both the bare "/42" and the
// plugin-qualified "/<uuid>/42" forms. Used to classify a rendered span as a
// tile embed (vs an external link / image).
func LeafTileIDFromHref(href string) string {
	_, leaf, ok := parseEmbedHref(href)
	if !ok {
		return ""
	}
	return leaf
}

// ResolveEmbedTileID parses an embed href to the qualified tile id the client
// tile cache is keyed by ("<uuid>/<local>"). A plugin-qualified href resolves
// DIRECTLY to its own "<uuid>/<local>" — anchorUUID is ignored — so an embed
// resolves to the same tile regardless of which plugin's doc holds it. A bare
// legacy href is re-qualified with the embedding doc's plugin (anchorUUID); with
// no anchor the bare leaf is returned unchanged.
//
// Returns "" when href is not a tile link.
func ResolveEmbedTileID(anchorUUID, href string) string {
	uuid, leaf, ok := parseEmbedHref(href)
	if !ok {
		return ""
	}
	if uuid != "" {
		return uuid + "/" + leaf // self-describing; anchor irrelevant
	}
	if anchorUUID == "" {
		return leaf
	}
	return anchorUUID + "/" + leaf
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
func Markdown(origin string, tileID string, alt string) string {
	return fmt.Sprintf("[%s](%s)", alt, HrefForTile(origin, tileID))
}

// DefaultAlt returns the fallback alt text used when a tile has no
// stored alt yet (a freshly-created text tile, a never-visited URL).
// Should be replaced by the per-tile stored alt at insert time when
// available.
func DefaultAlt(kind string, tileID string) string {
	seg := tileID
	if i := strings.LastIndexByte(tileID, '/'); i >= 0 {
		seg = tileID[i+1:]
	}
	return fmt.Sprintf("%s tile %s", kind, seg)
}

// Insert places `link` into `src` at byte offset `off`, padding with
// a single space on either side when the surrounding character isn't
// already whitespace. This keeps the markdown parser seeing the link
// as its own token even when the drop landed mid-word.
//
// off is clamped to [0, len(src)] and snapped back to a rune boundary, so an
// offset that lands mid-character neither splits a multibyte rune nor misreads
// a UTF-8 continuation byte as a non-space (which would add stray padding).
func Insert(src, link string, off int) string {
	if off < 0 {
		off = 0
	}
	if off > len(src) {
		off = len(src)
	}
	for off > 0 && off < len(src) && !utf8.RuneStart(src[off]) {
		off--
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
	FocusedTileID string
	LastTileID    string
	CurrentValue  string
	BlobCached    bool
	BlobContent   string
	// PendingEdit reports that the textarea buffer holds an edit of
	// LastTileID that has not been posted yet (typed within the save
	// debounce, or the debounce fired while focus was elsewhere and its
	// guard declined). On a rebind this edit is about to be destroyed.
	PendingEdit bool
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
	NewLastTileID string
	// FlushOldFirst: before applying this decision, persist CurrentValue
	// to LastTileID. Set on a rebind away from a tile whose buffer holds
	// a pending (unsaved) edit — clearing without flushing silently
	// destroys the user's typing (the fast-pane-switch data-loss bug).
	FlushOldFirst bool
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
			// The buffer still belongs to LastTileID here; if it carries an
			// unsaved edit it must be posted before the clear destroys it.
			FlushOldFirst: in.PendingEdit && in.LastTileID != "",
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
