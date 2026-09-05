// Package rpc declares the Go-side types for the Gridwell RPC service and
// their conversions to the proto wire form. The records and their
// mechanical mirrors are generated from data.proto into wire_gen.go; conv.go
// holds only the conversions a human has to write. The wire itself is
// Connect/gRPC on /gridwell.v1.Gridwell/<Method> — data.proto is the
// source of truth for the encoding.
package rpc

import (
	"math"
	"strings"
)

// Tile kinds. A tile is exactly one of these.
//
// The "interior" kinds — well, text, url, pane — live inside Gridwell. A
// shell is an "exit" kind: its contents reflect state owned by the host (a
// bash session), not by Gridwell; a plugin's wells (a directory, a
// process) are ordinary wells whose grid a plugin projects. The color
// grammar (red outline) follows.
const (
	KindWell  = "well"
	KindText  = "text"
	KindURL   = "url"
	KindShell = "shell"
	// KindPane is a durable layout: a tile whose content blob is a
	// serialized split-pane layout (the codec in api/panelayout).
	// Descending into it swaps the whole pane tree; ascending restores the
	// outer arrangement. The string is frozen into the store's CHECK.
	KindPane = "pane"
)

// IsWellKind reports whether a tile kind has a child grid that can be
// descended into. Only "well" qualifies. An exit well — one whose child grid
// lives in another plugin — is still a well, distinguished by its qualified
// child_grid_id, not by its kind. Shared by the store (path validation,
// refcount holdings) and the client (drop-target resolution).
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
// space: a file, process, or remote well, or a plugin dropped as a tile.
// Derived purely from the qualified ids: the well's own grid uuid versus its
// child grid uuid.
//
// A non-well, or a well with no child grid, is never an exit well; nor is a
// synthetic node with both grid ids empty, whose uuids are equal. But a
// synthetic node with an empty GridID and a qualified ChildGridID — the
// shape a plugin is rendered as, see PluginWellTile — is an exit well,
// because "" != "<uuid>". That is what makes a menu swatch preview and
// descend into the plugin's grid rather than draw as an inert interior
// well.
func IsExitWell(t *Tile) bool {
	return IsWellKind(t.Kind) && t.ChildGridID != "" &&
		UUIDOf(t.ChildGridID) != UUIDOf(t.GridID)
}

// NamespaceOf returns the id-space a qualified id belongs to: everything
// before its last segment ("n1" for "n1/7", "n1/c1" for "n1/c1/7", "" for a
// bare id). Two ids can only name the same store when their namespaces are
// equal, which is the test for "would a move cross a plugin boundary". No
// single-segment comparison answers that once ids chain through mounts.
func NamespaceOf(id string) string {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		return id[:i]
	}
	return ""
}

// LocalOf returns a qualified id's last segment: the id local to the owning
// plugin ("7" for "n1/7" or "n1/c1/7"; a bare id is its own local id).
// Complement of NamespaceOf: QualifyID(NamespaceOf(id), LocalOf(id)) == id
// for any qualified id. It is the display half of the codec, used for
// human-readable URL path segments and default alt text.
func LocalOf(id string) string {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		return id[i+1:]
	}
	return id
}

// PluginWellTile builds the synthetic exit-well tile a plugin is rendered as
// when it is not sitting in a real grid: the drag ghost and the menu swatch.
// It is a 1x1 well whose child grid is the plugin's qualified RootGridID, so
// IsExitWell is true — it has no owning grid uuid to match — and it previews
// and descends into that grid. One definition, so every use reads
// identically.
func PluginWellTile(pl PluginInfo) Tile {
	return Tile{
		Kind:        KindWell,
		W:           1,
		H:           1,
		AltText:     pl.Label,
		ChildGridID: pl.RootGridID,
		// A menu swatch is a link by nature: its child grid is the plugin's
		// own root, never this synthetic tile's grid. Mark it so it renders
		// dashed identically to a mounted plugin well.
		Reference: true,
		// The plugin's persisted root framing is this tile's framing. It is
		// one shape, so it carries across verbatim, and previewing or
		// descending through the synthetic tile lands at the left-off view.
		ViewCx:   pl.RootViewCx,
		ViewCy:   pl.RootViewCy,
		ViewZoom: pl.RootViewZoom,
	}
}

// PluginKindConnection is the Kind ConnectionRow stamps on a connection's
// menu row. It is the declaration that a row is a connection rather than a
// plugin, and it is the one fact readers consult, never the shape of the
// uuid. Minted here and nowhere else.
const PluginKindConnection = "connection"

// ConnectionRow presents a connection as a menu row: the one shape every
// menu flow (click-descend, drag-link, health, root-view persistence)
// already handles. Its Kind is PluginKindConnection. A pending connection is
// rootless with its failure carried as StatusDetail, then InfoError, so
// health reads the declared kind as waiting rather than broken.
//
// The globe is declared here, the one place connection rows are minted, so
// every face a connection wears — its swatch, its drag ghost, its crumb —
// reads it as a declaration like any plugin's, and no renderer needs to know
// what kind of row it has.
func ConnectionRow(c ConnectionInfo) PluginInfo {
	return PluginInfo{
		UUID: c.UUID, Kind: PluginKindConnection, Label: c.Label, Glyph: GlyphGlobe,
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
// home_grid_id, falling back to the first rooted row for a node that does
// not send the field. One derivation; every "empty anchor means home"
// reader goes through it.
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
// descend into with a content descent, which sets pane.TextFocus, rather
// than a grid descent: text, url, and shell. The client's click-to-descend
// routing and its URL-restore walk share it, so the set is spelled out once.
// If the two drift, a descent encoded into the URL is silently dropped on
// reload.
func IsContentDescentKind(kind string) bool {
	return kind == KindText || kind == KindURL || kind == KindShell
}

// IsWorkspaceKind reports whether a tile kind is a pane tile: the third
// descent class. A pane-tile descent is neither a grid descent (IsWellKind,
// which pushes onto pane.Path) nor a content descent (IsContentDescentKind,
// which sets pane.TextFocus); it swaps the whole pane tree and pushes a
// level. The three predicates partition the descendable kinds, and the pin
// test keeps the sets disjoint and total so a new kind cannot fall through a
// descent or URL-restore dispatch.
func IsWorkspaceKind(kind string) bool {
	return kind == KindPane
}

// EntryPlugin shapes a plugin's root MenuEntry as a pseudo-plugin: the
// entry's grid as the root and its label and glyph as the face, so every
// downstream flow (swatch, ghost, click-descend, drag-link, the bar's door
// identity) takes the ordinary plugin path. The handshake root view belongs
// to the main root grid, so it is zeroed and an entry grid opens at the
// default framing.
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

// The plugin glyph vocabulary (InfoResponse.glyph, PluginInfo.Glyph):
// declared by the plugin, rendered by the client, with a name the client does
// not know falling back to the generic globe, so a third-party plugin
// degrades politely without either side learning names. A row that declares
// NOTHING is a different case: it takes the grid face, since a plugin serves
// grids (client/door.RowGlyph owns that default). GlyphGlobe is the far side:
// a connection declares it, in ConnectionRow.
const (
	GlyphFolder  = "folder"
	GlyphProcess = "process"
	GlyphWell    = "well"
	GlyphTrash   = "trash"
	GlyphGlobe   = "globe"
)

// Text-tile display modes.
const (
	TextModeRendered = "rendered"
	TextModeText     = "text"
)

// WebContent reports whether this tile presents as web content: a url tile,
// at its own address, or a serves_page tile, at the /content/ door address.
// It is the single classification every url-tile semantic keys off — live
// native view on a desktop host, open-in-new-tab on a browser host, frozen
// preview image — so the two shapes cannot diverge gesture by gesture.
func (t *Tile) WebContent() bool {
	return t.Kind == KindURL || t.ServesPage
}

// TextDocument reports whether this tile's content is its own document body:
// the tile that fetches a blob, carries text framing, and shows the markdown
// face. It is the complement of the page arm inside a text kind — a
// serves_page row is a file whose presentation is a page, so it has no body
// of its own to descend into. Every "is this the document" question reads
// this, so the shim never spells the kind-and-not-page pair itself.
func (t *Tile) TextDocument() bool {
	return t.Kind == KindText && !t.ServesPage
}

// PageContent reports whether this tile presents at the /content/ door: the
// serves_page arm of WebContent, and its complement. A page tile holds no
// persisted url state, no content zoom, and no freeze intent — those belong
// to the owning plugin, which holds no node fact — so the address is derived
// at use time (PageURL). A url tile is never a page tile however its row is
// flagged: its own address wins.
func (t *Tile) PageContent() bool {
	return t.ServesPage && t.Kind != KindURL
}

// LeafLink reports whether this row is a leaf link: one content tile shown in
// a second place, owning no bytes of its own. ContentID answers "whose
// content" and is the read-through every content operation takes; this names
// the question where the answer is not an id — whether to resolve at all,
// whether to prompt, whether the row is the content's home.
func (t *Tile) LeafLink() bool {
	return t.LinkTargetID != ""
}

// PageURL builds the /content/ door address for a tile: the one place the
// URL grammar is written on the client side. The server's parseContentPath
// is its mirror, and a seam test pins that they agree. The trailing slash is
// load-bearing, because relative subresource URLs inside a served page
// resolve against the directory.
func PageURL(origin, contentToken, tileID string) string {
	return origin + "/content/" + contentToken + "/" + tileID + "/"
}

// The text_presentation vocabulary.
const (
	TextPresentationPlain    = "plain"
	TextPresentationRendered = "rendered"
	TextPresentationBoth     = "both"
)

// ContentID returns the tile id that owns this tile's content: a leaf link's
// target, or the tile's own id. Every client content operation — body fetch,
// edit buffer, save routing, preview fetch, shell session, pane layout —
// keys by this, so a link and its target share one content fact and a write
// can never land on a link row, which owns no bytes and which the store
// refuses. It is the single resolution point for read-through.
func (t *Tile) ContentID() string {
	if t.LinkTargetID != "" {
		return t.LinkTargetID
	}
	return t.ID
}

// The read and mutation request and response shapes that mirror a proto
// message field for field are generated into wire_gen.go: GetGridResponse,
// CloneTileRequest, PlaceTileRequest, DeleteTileRequest, ShellSessionAlive*,
// the event payloads, PluginInfo and ConnectionInfo among them. What stays
// here is the shapes that are not a message mirror: the typed create and set
// sugar over the unified CreateTile and SetTile verbs, the embedded Framing,
// and the Event discriminator over the proto's oneof.
//
// The Client builds the remaining requests as proto directly, so they have
// no Go twin here.

// Creates: no Version (the tile doesn't exist yet).

// CreateWellRequest is a typed create. On the wire every create is a single
// CreateTile carrying a Tile; the Client exposes typed sugar over it and the
// home store keeps these as its internal create API.
type CreateWellRequest struct {
	GridID string `json:"grid_id"`
	X      int64  `json:"x"`
	Y      int64  `json:"y"`
	W      int64  `json:"w"`
	H      int64  `json:"h"`
	// ChildGridID, when set, makes this an exit well: a doorway onto an
	// existing grid named by its qualified id — a mounted node, an fs or
	// proc grid, or, when ctrl + right-drag links a well, another doorway
	// onto a grid in the same namespace. Label is the display name for such
	// a well. Empty means an ordinary interior well, which allocates its own
	// child grid.
	ChildGridID string `json:"child_grid_id,omitempty"`
	Label       string `json:"label,omitempty"`
	// Framing seeds an exit well's framing at creation: a plugin link
	// dropped from the + menu starts at the plugin's persisted root view.
	// Zero zoom means never visited, so the default view. Ignored for
	// interior wells, which always start unframed.
	Framing
}

// CreateLeafLinkRequest creates a leaf link: a text, url, shell, or pane tile
// whose content lives in another tile — what a ctrl + right-drag makes
// anywhere, and what a cross-plugin left-drag makes at a namespace boundary.
// Kind is the target's kind, LinkTargetID is the qualified "<uuid>/<tile-id>"
// reference — qualified in every namespace, so the target may be a neighbor
// in the same one — and Label is the link's local alt_text.
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
	// Label is the pane tile's name: its alt_text, and the bar's crumb.
	Label string `json:"label,omitempty"`
	// Data is the optional initial layout blob. Empty means never arranged,
	// and descent installs the default single pane.
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

// CreateShellRequest creates a shell tile. The shell session lives in a
// Gridwell-private tmux session keyed by tile id, so it persists across
// ascents until the tile is deleted or the machine reboots.
type CreateShellRequest struct {
	GridID string `json:"grid_id"`
	X      int64  `json:"x"`
	Y      int64  `json:"y"`
	W      int64  `json:"w"`
	H      int64  `json:"h"`
}

// Mutations. Only the content writes carry a Version claim: WriteContent and
// RenameTile. Version means the user's content bytes changed. Framing,
// automatic captures, and layout (place, clone, delete) are
// last-writer-wins and carry no claim; the server returns 409 and
// ErrVersionConflict only for a stale content claim.

// Framing is the one shape of "how this grid looked when I left it through
// this doorway": a float center in the grid's own coordinates plus a
// pane-size-independent zoom, the intrinsic ratio live over overtake, so a
// window resize never moves a saved view. Zoom == 0 is the one "never
// visited" convention, and Cx and Cy carry no meaning then.
type Framing struct {
	Cx   float64 `json:"cx,omitempty"`
	Cy   float64 `json:"cy,omitempty"`
	Zoom float64 `json:"zoom,omitempty"`
}

// framingEpsilon is how close two framings must be to count as the same
// picture. Below it a write is noise: float jitter in an animated or
// re-derived viewport, not a place the user chose. One cell-thousandth is
// far under a screen pixel at any usable zoom.
const framingEpsilon = 0.001

// SameAs reports whether f and g describe the same framing, within
// framingEpsilon. It is the one "did the user actually move?" rule: the
// no-op guard every persister consults, so a quiet settle tick never churns
// the store.
func (f Framing) SameAs(g Framing) bool {
	return math.Abs(f.Cx-g.Cx) < framingEpsilon &&
		math.Abs(f.Cy-g.Cy) < framingEpsilon &&
		math.Abs(f.Zoom-g.Zoom) < framingEpsilon
}

// SetFramingRequest persists a Framing onto the row that owns it. Exactly
// one target is set: TileID names the doorway tile a grid was entered
// through (a well, interior, exit, or link — each doorway keeps its own
// framing), and RootGridID names a root grid, which has no doorway. Framing
// carries no claim and no version bump; it is last-writer-wins, so there is
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

// SetURLStateRequest freezes a live url tile — preview JPEG, address, title,
// and history — when its native view is torn down on ascent. Every field is
// a capture of what the live surface was observed to be, so it carries no
// version claim and makes no bump. It is an in-place write to this tile's
// row. Empty jpeg, url, and title fields are skipped.
type SetURLStateRequest struct {
	TileID string `json:"tile_id"`
	JPEG   []byte `json:"jpeg"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	// History is the JSON back-stack captured at freeze. "" leaves the
	// stored history untouched, so a partial capture cannot clobber it.
	History string `json:"history,omitempty"`
}

// SetContentZoomRequest persists a tile's content scale (framing; no claim,
// no bump).
type SetContentZoomRequest struct {
	TileID      string  `json:"tile_id"`
	ContentZoom float64 `json:"content_zoom"`
}

// SetURLFrozenRequest persists the user's standing freeze on a url tile.
// Framing: no claim, no bump.
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
