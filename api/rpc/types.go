// Package rpc declares the Go-side types for the Gridwell RPC service and
// their conversions to the proto wire form (conv.go). The wire itself is
// Connect/gRPC on /gridwell.v1.Gridwell/<Method> — data.proto is the
// source of truth for the encoding.
package rpc

import (
	"math"
	"strings"
)

// Tile kinds. A tile is exactly one of these.
//
// The "interior" kinds — well, text, url — live inside Gridwell. file-well,
// process-well, and shell are "exit" kinds: their contents reflect state
// owned by the host (the filesystem, the process table, a bash session),
// not by Gridwell. The color grammar (red outline) follows.
const (
	KindWell  = "well"
	KindText  = "text"
	KindURL   = "url"
	KindShell = "shell"
	// KindPane is a durable workspace: a tile whose content blob is a
	// serialized split-pane layout (the LayoutV1 codec in client/pane).
	// Descending into it swaps the whole pane tree; ascending restores the
	// outer arrangement. The string is frozen into the localdb CHECK.
	KindPane = "pane"
)

// IsWellKind reports whether a tile kind has a child grid that can be
// descended into. Only "well" qualifies — an exit well (one whose child grid
// lives in another plugin) is still a well; it is distinguished by its
// qualified child_grid_id, not by its kind. Shared by the store (path
// validation, refcount holdings) and the client (drop-target resolution).
func IsWellKind(kind string) bool {
	return kind == KindWell
}

// The "<uuid>/<local>" shape is Gridwell's one cross-plugin id convention: a
// plugin-scoped local id prefixed with the owning plugin's UUID. QualifyID,
// SplitID, UUIDOf, and NamespaceOf are its only encode/decode points — shared
// by the server (id qualification and routing) and the client (cache lookup,
// exit-well classification) so the two can never disagree on the format.

// QualifyID builds a qualified id "<uuid>/<local>" from its parts.
func QualifyID(uuid, local string) string { return uuid + "/" + local }

// QualifyNS prepends one hop segment to a NAMESPACE chain — the node_ns
// stamping rule: an empty chain ("the node you asked") gains just the
// hop; a non-empty chain gains "<hop>/" in front. Distinct from
// QualifyID because a namespace may legitimately be empty.
func QualifyNS(hop, ns string) string {
	if ns == "" {
		return hop
	}
	return hop + "/" + ns
}

// SplitID splits a qualified id at its FIRST separator — the one-hop routing
// peel. uuid names the plugin this hop routes to; rest is the id from that
// plugin's perspective (itself possibly a chain). ok is false when the id has
// no non-empty first segment (a bare/local id, or a degenerate leading "/").
func SplitID(id string) (uuid, rest string, ok bool) {
	if i := strings.IndexByte(id, '/'); i > 0 {
		return id[:i], id[i+1:], true
	}
	return "", "", false
}

// UUIDOf returns the plugin-uuid segment of a qualified id ("<uuid>/<local>"),
// or "" when the id is bare/unqualified. Defined on SplitID so the two can
// never disagree.
func UUIDOf(id string) string {
	uuid, _, _ := SplitID(id)
	return uuid
}

// IsExitWell reports whether a well tile's child grid lives in a different
// plugin than the well itself — descending it leaves the current plugin's id
// space (a file/process/remote well, or a plugin mounted as a launcher tile).
// Derived purely from the qualified ids: the well's own grid uuid versus its
// child grid uuid.
//
// A non-well, or a well with no child grid, is never an exit well; nor is a
// synthetic node with both grid ids empty (uuids equal). But a synthetic node
// with an empty GridID and a qualified ChildGridID — exactly the shape the
// launcher renders a plugin as (see PluginWellTile) — IS an exit well, because
// "" != "<uuid>". That is what makes a launcher tile preview and descend into
// the plugin's grid rather than draw as an inert interior well.
func IsExitWell(t *Tile) bool {
	return IsWellKind(t.Kind) && t.ChildGridID != "" &&
		UUIDOf(t.ChildGridID) != UUIDOf(t.GridID)
}

// NamespaceOf returns the id-space a qualified id belongs to: everything
// before its LAST segment ("uuid" for "uuid/7", "ssh1/rp1" for "ssh1/rp1/7",
// "" for a bare id). Two ids can only ever name the same store when their
// namespaces are equal — the test for "would a move cross a plugin
// boundary", which no single-uuid comparison answers once ids chain
// through node mounts.
func NamespaceOf(id string) string {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		return id[:i]
	}
	return ""
}

// LocalOf returns a qualified id's LAST segment — the id local to the owning
// plugin ("7" for "uuid/7" or "ssh1/rp1/7"; a bare id is its own local id).
// Complement of NamespaceOf: QualifyID(NamespaceOf(id), LocalOf(id)) == id
// for any qualified id. The display half of the codec — human-readable URL
// path segments, default alt text, node-grid tile ids.
func LocalOf(id string) string {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		return id[i+1:]
	}
	return id
}

// PluginWellTile builds the synthetic exit-well tile a plugin is rendered as
// when it isn't sitting in a real grid: the drag ghost, the menu swatch, and
// the launcher start-page tile (whose preview is the plugin's root grid). A 1×1
// well whose child grid is the plugin's qualified RootGridID — so IsExitWell is
// true (it has no owning grid uuid to match) and it previews / descends into
// that grid. One definition so all three uses read identically.
func PluginWellTile(pl PluginInfo) Tile {
	return Tile{
		Kind:        KindWell,
		W:           1,
		H:           1,
		AltText:     pl.Label,
		ChildGridID: pl.RootGridID,
		// A launcher/menu swatch is a link by nature — its child grid is the
		// plugin's own root, never this (synthetic) tile's grid. Mark it so it
		// renders dashed identically to a mounted plugin well.
		Reference: true,
		// The plugin's persisted root framing IS this tile's framing — one
		// shape, so it carries across verbatim — and previewing or
		// descending through the synthetic tile lands at the left-off view.
		ViewCx:   pl.RootViewCx,
		ViewCy:   pl.RootViewCy,
		ViewZoom: pl.RootViewZoom,
	}
}

// PluginKindConnection is the Kind ConnectionRow stamps on a connection's
// menu row. It is the DECLARATION that a row is a connection rather than a
// plugin — the one fact readers consult (client/pluginhealth), never the
// shape of the uuid. Minted here and nowhere else.
const PluginKindConnection = "connection"

// ConnectionRow presents a connection as a menu row — the one shape every
// menu flow (click-descend, drag-link, health, root-view persistence)
// already handles. Kind PluginKindConnection; a pending one is rootless
// with its failure as StatusDetail → InfoError (pluginhealth reads the
// declared kind as "waiting", not "broken").
func ConnectionRow(c ConnectionInfo) PluginInfo {
	return PluginInfo{
		UUID: c.UUID, Kind: PluginKindConnection, Label: c.Label,
		RootGridID: c.RootGridID, InfoError: c.StatusDetail,
		RootViewCx: c.RootViewCx, RootViewCy: c.RootViewCy, RootViewZoom: c.RootViewZoom,
	}
}

// MenuRows is the + menu's top row for a handshake: the node's plugins
// (home first) followed by its connections.
func MenuRows(l PluginList) []PluginInfo {
	out := make([]PluginInfo, 0, len(l.Plugins)+len(l.Connections))
	out = append(out, l.Plugins...)
	for _, c := range l.Connections {
		out = append(out, ConnectionRow(c))
	}
	return out
}

// HomeGrid picks the qualified grid id that "/" means: the handshake's
// home_grid_id (a FIELD, docs/one-node.md), falling back to the first
// rooted row for a node that predates the field. One derivation; every
// "empty anchor means home" reader goes through it.
func HomeGrid(l PluginList) string {
	if l.HomeGridID != "" {
		return l.HomeGridID
	}
	for _, pl := range l.Plugins {
		if pl.RootGridID != "" {
			return pl.RootGridID
		}
	}
	return ""
}

// IsContentDescentKind reports whether a tile kind is a content tile you
// descend into via a *text-focus* descent (it sets pane.TextFocus) rather than
// a grid descent — text, url, and shell. Shared by the client's click-to-descend
// routing and its URL-restore walk so the set is spelled out once: when those
// two drifted, a shell descent encoded into the URL was silently dropped on
// reload (the restore walk omitted shell).
func IsContentDescentKind(kind string) bool {
	return kind == KindText || kind == KindURL || kind == KindShell
}

// IsWorkspaceKind reports whether a tile kind is a pane tile — the THIRD
// descent class. A workspace descent is neither a grid descent (IsWellKind:
// push onto pane.Path) nor a text-focus descent (IsContentDescentKind: set
// pane.TextFocus); it swaps the whole pane tree and pushes a workspace
// frame. The three predicates partition the descendable kinds; the pin test
// keeps the sets disjoint and total so a new kind cannot silently fall
// through a descent or URL-restore dispatch.
func IsWorkspaceKind(kind string) bool {
	return kind == KindPane
}

// Grid source kinds. NULL ("") means a regular Gridwell-owned grid. fs
// means the grid's tile list is reconciled against a host directory; proc
// means against the host process table. SourceID carries the path or PID.
const (
	GridSourceFS   = "fs"
	GridSourceProc = "proc"
)

// EntryPlugin shapes a plugin ROOT MenuEntry as a PSEUDO-PLUGIN (#258):
// the entry's grid as the root, its label/glyph as the face, no instance
// grid — so every downstream flow (swatch, ghost, click-descend,
// drag-link, the bar's door identity) is the battle-tested plugin path.
// The handshake root view belongs to the MAIN root grid, so it is
// zeroed: an entry grid opens at the default framing.
func EntryPlugin(pl PluginInfo, e MenuEntry) PluginInfo {
	pseudo := pl
	pseudo.RootGridID = e.GridID
	pseudo.RootViewCx, pseudo.RootViewCy, pseudo.RootViewZoom = 0, 0, 0
	if e.Label != "" {
		pseudo.Label = e.Label
	}
	if e.Glyph != "" {
		pseudo.Glyph = e.Glyph
	}
	return pseudo
}

// The plugin glyph vocabulary (InfoResponse.glyph / PluginInfo.Glyph):
// declared by the plugin, rendered by the client, with anything unknown
// falling back to the generic globe — a third-party plugin degrades
// politely without either side learning names.
const (
	GlyphFolder  = "folder"
	GlyphProcess = "process"
	GlyphWell    = "well"
	GlyphTrash   = "trash"
)

// Text-tile display modes.
const (
	TextModeRendered = "rendered"
	TextModeText     = "text"
)

// (The Path type is gone — 2026-07-26 contraction: every mutation is
// id-addressed + version-claimed.)

// WebContent reports whether this tile PRESENTS as web content: a url tile
// (its own address) or a serves_page tile (the /content/ door address). The
// single classification every url-tile semantic keys off — live native view
// on a desktop host, open-in-new-tab on a browser host, preview-image
// frozen face — so the two shapes can never diverge gesture by gesture.
func (t *Tile) WebContent() bool {
	return t.Kind == KindURL || t.ServesPage
}

// PageURL builds the /content/ door address for a tile — the ONE place the
// URL grammar is written on the client side (the server's parseContentPath
// is its mirror; a seam test pins that they agree). The trailing slash is
// load-bearing: relative subresource URLs inside a served page resolve
// against the directory.
func PageURL(origin, contentToken, tileID string) string {
	return origin + "/content/" + contentToken + "/" + tileID + "/"
}

// The text_presentation vocabulary (decision 2026-08-13).
const (
	TextPresentationPlain    = "plain"
	TextPresentationRendered = "rendered"
	TextPresentationBoth     = "both"
)

// ContentID returns the tile id that OWNS this tile's content: a leaf link's
// target, or the tile's own id. Every client content operation — body fetch,
// edit buffer, save routing, preview fetch, shell session, workspace layout —
// keys by this, so a link and its target (and every sibling link) share ONE
// content fact and a write can never land on a link row (which owns no
// bytes; the store refuses it). The single resolution point for read-through.
func (t *Tile) ContentID() string {
	if t.LinkTargetID != "" {
		return t.LinkTargetID
	}
	return t.ID
}

// The read/mutation request and response shapes that mirror a proto message
// field-for-field are GENERATED (wire_gen.go) — GetGridResponse,
// CloneTileRequest, PlaceTileRequest, DeleteTileRequest, ShellSessionAlive*,
// the event payloads, PluginInfo and ConnectionInfo among them. What stays
// here is the shapes that are NOT a message mirror: the store's typed create
// and set sugar over the unified CreateTile/SetTile verbs, the embedded
// Framing, and the Event discriminator over the proto's oneof.
//
// (GetGridRequest, GetTilePreviewRequest/Response, TileResponse,
// SubscribeRequest and DeleteTileResponse were mirrors nothing read: the
// Client builds those requests as proto directly. Deleted 2026-08-29 rather
// than generated — a copy no one uses is still a copy.)

// Creates: no Version (the tile doesn't exist yet).

// CreateWellRequest is a typed create. On the wire every create is a single
// CreateTile carrying a Tile; the Client exposes typed sugar (CreateWell, …)
// over it and the localdb store keeps these as its internal create API.
type CreateWellRequest struct {
	GridID string `json:"grid_id"`
	X      int64  `json:"x"`
	Y      int64  `json:"y"`
	W      int64  `json:"w"`
	H      int64  `json:"h"`
	// ChildGridID, when set, makes this an exit well pointing at an existing
	// grid in another plugin (a mounted DB, an fs/proc grid). Label is the
	// display name for such a well. Empty → an ordinary interior well.
	ChildGridID string `json:"child_grid_id,omitempty"`
	Label       string `json:"label,omitempty"`
	// Framing seeds an exit well's framing at creation (e.g. a plugin link
	// dropped from the + menu starts at the plugin's persisted root view,
	// the same framing a node-grid tile shows). Zero zoom = never visited,
	// the default view. Ignored for interior wells, which always start
	// unframed.
	Framing
}

// (The unconfigured plugin well is gone — 2026-08-29. The instance
// picker retired 2026-08-23 (connections are config rows), so nothing
// could mint or adopt one: CreateWellRequest.ConfigurePluginID, the
// CreateTile configure arm, SetTile's adopt arm, store's
// CreatePluginWell/AdoptChildGrid, Tile.ConfigurePluginID and the
// column itself (schema v10) all went.)

// CreateLeafLinkRequest creates a LEAF LINK: a text/url/shell/pane tile whose
// content lives in another plugin's tile (the cross-plugin left-drag). Kind is
// the target's kind; LinkTargetID is the qualified "<uuid>/<tile-id>"
// reference; Label is the link's local alt_text (usually the source's).
type CreateLeafLinkRequest struct {
	GridID       string `json:"grid_id"`
	X            int64  `json:"x"`
	Y            int64  `json:"y"`
	W            int64  `json:"w"`
	H            int64  `json:"h"`
	Kind         string `json:"kind"`
	LinkTargetID string `json:"link_target_id"`
	Label        string `json:"label,omitempty"`
}

type CreateTextRequest struct {
	GridID string `json:"grid_id"`
	X      int64  `json:"x"`
	Y      int64  `json:"y"`
	W      int64  `json:"w"`
	H      int64  `json:"h"`
	Data   []byte `json:"data"`
}

type CreatePaneRequest struct {
	GridID string `json:"grid_id"`
	X      int64  `json:"x"`
	Y      int64  `json:"y"`
	W      int64  `json:"w"`
	H      int64  `json:"h"`
	// Label is the workspace name (alt_text; the bottom bar's breadcrumb).
	Label string `json:"label,omitempty"`
	// Data is the optional initial layout blob; empty = never arranged
	// (descent installs the default single pane).
	Data []byte `json:"data,omitempty"`
}

type CreateURLRequest struct {
	GridID string `json:"grid_id"`
	X      int64  `json:"x"`
	Y      int64  `json:"y"`
	W      int64  `json:"w"`
	H      int64  `json:"h"`
	URL    string `json:"url"`
}

// CreateShellRequest creates a shell tile. The bash session is not
// started until the user refreshes (matches the URL tile model — drop
// + descend show the frozen preview placeholder until explicitly
// activated). Once activated, the bash lives in a gridwell-private
// tmux session keyed by tile id and persists across ascents until
// the tile is deleted (or the machine reboots).
type CreateShellRequest struct {
	GridID string `json:"grid_id"`
	X      int64  `json:"x"`
	Y      int64  `json:"y"`
	W      int64  `json:"w"`
	H      int64  `json:"h"`
}

// Mutations. Only the CONTENT writes carry a Version claim — WriteContent
// and RenameTile (2026-08-29, docs/simplify-plan.md S5: version means "the
// user's content bytes changed"). Framing, automatic captures and LAYOUT
// (place / clone / delete) are last-writer-wins and carry none; the server
// returns 409 / ErrVersionConflict only for a stale content claim.

// Framing is the ONE shape of "how this grid looked when I left it through
// this doorway": a float CENTER in the grid's own coordinates plus a
// pane-size-independent zoom — the intrinsic ratio live/overtake, so a
// window resize never moves a saved view. Zoom == 0 is the one
// "never visited" convention; Cx/Cy carry no meaning then.
type Framing struct {
	Cx   float64 `json:"cx,omitempty"`
	Cy   float64 `json:"cy,omitempty"`
	Zoom float64 `json:"zoom,omitempty"`
}

// framingEpsilon is how close two framings must be to count as the same
// picture. Below it a write would be noise — float jitter in an animated
// or re-derived viewport, not a place the user chose. One cell-thousandth
// is far under a screen pixel at any usable zoom.
const framingEpsilon = 0.001

// SameAs reports whether f and g describe the same framing, within
// framingEpsilon. It is the ONE "did the user actually move?" rule — the
// no-op guard every persister consults, so a quiet settle tick never
// churns the store.
func (f Framing) SameAs(g Framing) bool {
	return math.Abs(f.Cx-g.Cx) < framingEpsilon &&
		math.Abs(f.Cy-g.Cy) < framingEpsilon &&
		math.Abs(f.Zoom-g.Zoom) < framingEpsilon
}

// SetFramingRequest persists a Framing onto the row that owns it. Exactly
// one target: TileID names the DOORWAY tile a grid was entered through (a
// well — interior, exit, or link; each doorway keeps its own framing),
// RootGridID a ROOT grid, which has no doorway. Framing only — no claim and
// no version bump: framing is last-writer-wins by design, so there is
// nothing here for a racing capture to conflict with.
type SetFramingRequest struct {
	TileID     string `json:"tile_id,omitempty"`
	RootGridID string `json:"root_grid_id,omitempty"`
	Framing
}

// SetTextViewRequest persists a text tile's framed window and rendered/text
// mode. Framing: no claim, no bump.
type SetTextViewRequest struct {
	TileID   string `json:"tile_id"`
	TextX    int64  `json:"text_x"`
	TextY    int64  `json:"text_y"`
	TextW    int64  `json:"text_w"`
	TextH    int64  `json:"text_h"`
	TextMode string `json:"text_mode"`
}

// SetShellPreviewRequest stores the JPEG frame captured at ascent as
// the frozen preview. Bytes are hash-deduped through the blobs table. An
// automatic capture: no claim, no version bump.
type SetShellPreviewRequest struct {
	TileID string `json:"tile_id"`
	JPEG   []byte `json:"jpeg"`
}

// SetURLStateRequest freezes a live URL tile (preview JPEG + address +
// title + history) when its Electron WebContentsView is torn down on ascend.
// Every field is a CAPTURE — what the live surface was observed to be — so
// it carries no version claim and makes no bump (docs/simplify-plan.md S5);
// it is an in-place write to this tile's row (copy-on-clone: clones are
// independent, so there is no fork). Empty jpeg/url/title fields are skipped.
type SetURLStateRequest struct {
	TileID string `json:"tile_id"`
	JPEG   []byte `json:"jpeg"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	// History is the JSON back-stack captured at freeze ("" = leave the
	// stored history untouched — a partial capture must not clobber it).
	History string `json:"history,omitempty"`
}

// SetContentZoomRequest persists a tile's content scale (framing; no claim,
// no bump).
type SetContentZoomRequest struct {
	TileID      string  `json:"tile_id"`
	ContentZoom float64 `json:"content_zoom"`
}

// SetURLFrozenRequest persists the user's standing freeze on a url tile
// (issue #237; framing, no claim, no bump).
type SetURLFrozenRequest struct {
	TileID string `json:"tile_id"`
	Frozen bool   `json:"frozen"`
}

// Event stream.

type EventKind string

const (
	EventGridChanged  EventKind = "grid_changed"
	EventTileChanged  EventKind = "tile_changed"
	EventTileRemoved  EventKind = "tile_removed"
	EventPluginHealth EventKind = "plugin_health"
)

type Event struct {
	Kind         EventKind     `json:"kind"`
	GridChanged  *GridChanged  `json:"grid_changed,omitempty"`
	TileChanged  *TileChanged  `json:"tile_changed,omitempty"`
	TileRemoved  *TileRemoved  `json:"tile_removed,omitempty"`
	PluginHealth *PluginHealth `json:"plugin_health,omitempty"`
}
