// Package rpc declares the Go-side types for the Gridwell RPC service and
// their conversions to the proto wire form (conv.go). The wire itself is
// Connect/gRPC on /gridwell.v1.Gridwell/<Method> — data.proto is the
// source of truth for the encoding.
package rpc

import "strings"

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
		// The plugin's persisted root view IS this tile's framing — the same
		// mapping the node grid serves — so previewing and descending through
		// the synthetic tile land at the left-off view.
		ViewX:    int64(pl.RootViewCx),
		ViewY:    int64(pl.RootViewCy),
		ViewZoom: pl.RootViewZoom,
	}
}

// HomeGrid picks the qualified grid id that "/" means: the root grid of the
// FIRST configured plugin that has one (server.yaml order — the owner's
// "shown first" pick, 2026-07-19, reversing the launcher-as-landing-page
// decision), falling back past broken/rootless plugins, and finally to the
// node grid so a node with no usable plugin still lands on its plugin list.
// One derivation; every "empty anchor means home" reader goes through it.
func HomeGrid(plugins []PluginInfo, nodeRoot string) string {
	for _, pl := range plugins {
		if pl.RootGridID != "" {
			return pl.RootGridID
		}
	}
	return nodeRoot
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
	// GridSourceNode marks a node grid (a plugin-list landing page —
	// stamped by nodegrid.go; a mount's landing page arrives with it too).
	GridSourceNode = "node"
)

// CreateEntryTileRequest mints a tile from a plugin MenuEntry (#258).
type CreateEntryTileRequest struct {
	GridID     string
	Kind       string
	MenuEntry  string
	Label      string
	X, Y, W, H int64
}

// MenuEntry is one plugin-declared (+) menu entry (issue #258) — see
// the proto's MenuEntry for the full contract. GridID set = a ROOT
// entry (plugin-swatch semantics over that grid); Kind set = a CREATION
// entry (drop mints a tile carrying Tile.MenuEntry = ID; ParamSchema,
// when non-empty, is prompted on first descent and committed as
// content).
type MenuEntry struct {
	ID          string `json:"id"`
	Label       string `json:"label,omitempty"`
	Glyph       string `json:"glyph,omitempty"`
	Color       string `json:"color,omitempty"`
	Kind        string `json:"kind,omitempty"`
	ParamSchema string `json:"param_schema,omitempty"`
	GridID      string `json:"grid_id,omitempty"`
}

// EntryPlugin shapes a plugin ROOT MenuEntry as a PSEUDO-PLUGIN (#258):
// the entry's grid as the root, its label/glyph as the face, no instance
// grid — so every downstream flow (swatch, ghost, click-descend,
// drag-link, the bar's door identity) is the battle-tested plugin path.
// The handshake root view belongs to the MAIN root grid, so it is
// zeroed: an entry grid opens at the default framing.
func EntryPlugin(pl PluginInfo, e MenuEntry) PluginInfo {
	pseudo := pl
	pseudo.RootGridID = e.GridID
	pseudo.InstanceGridID = ""
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

// Grid is the persistent unit of canvas. Tiles live in grids; wells point at
// child grids. The root grid has no parent.
//
// SourceKind is "" for a regular Gridwell-owned grid, "fs" for a grid whose
// tile list is reconciled against a host directory, "proc" for the process
// table. SourceID carries the path or PID; clients use SourceKind to pick
// the red color theme on descent.
type Grid struct {
	ID         string `json:"id"`
	ObjectID   string `json:"object_id"`
	Version    int64  `json:"version"`
	SourceKind string `json:"source_kind,omitempty"`
	SourceID   string `json:"source_id,omitempty"`
	// Writable is the owning plugin's per-grid mutation capability, stamped
	// by the serving node (wire-only). The client's "show the + palette"
	// gate reads this — per grid, because one local mount (ssh) can front
	// many remote plugins with differing capabilities.
	Writable bool `json:"writable,omitempty"`
	// MenuEntries is the owning plugin's declared (+) menu additions for
	// this grid (issue #258): root entries (GridID set — a second plugin
	// root like local's trashcan) and creation entries (Kind set — a
	// parameterized tool like fs's search). Stamped by the serving node;
	// verbatim through transit with GridID prefixed per hop.
	MenuEntries []MenuEntry `json:"menu_entries,omitempty"`
	// NodeNS is the namespace chain of the NODE serving this grid, from
	// this receiver's perspective: "" = the node you are talking to;
	// "<transit>" or deeper through mounts. The one owner of "which node
	// is this pane inside" — the + menu context key and the ListPlugins
	// routing namespace (remote-menu, 2026-08-16).
	NodeNS string `json:"node_ns,omitempty"`
	// Stale marks a response served from a mount's offline cache (#256):
	// the remembered answer, not the live one. Wire-only, mountcache-set.
	Stale bool `json:"stale,omitempty"`
	// ScratchGridID is the owning plugin's ephemeral-url scratch grid,
	// qualified for this receiver (chained through mounts); "" = none.
	// Same stamping rule as Writable — the fact rides ON the grid.
	ScratchGridID string `json:"scratch_grid_id,omitempty"`
	// CreateSchemas maps a tile kind to the owning plugin's JSON Schema for
	// that kind's creation parameters (issue #198). Same stamping rule as
	// Writable; verbatim through transit. Empty = no parameters needed.
	CreateSchemas map[string]string `json:"create_schemas,omitempty"`
}

// Tile is the persistent unit of content in a grid. Kind selects which subset
// of the optional fields is meaningful.
type Tile struct {
	ID       string `json:"id"`
	ObjectID string `json:"object_id"`
	Version  int64  `json:"version"`
	GridID   string `json:"grid_id"`
	Kind     string `json:"kind"`
	X        int64  `json:"x"`
	Y        int64  `json:"y"`
	W        int64  `json:"w"`
	H        int64  `json:"h"`
	// well-only: ViewX/Y/Zoom is the child grid's framing — the preview
	// frame, the descent target, and the ascent return value.
	ViewX       int64   `json:"view_x,omitempty"`
	ViewY       int64   `json:"view_y,omitempty"`
	ViewZoom    float64 `json:"view_zoom,omitempty"`
	ChildGridID string  `json:"child_grid_id,omitempty"`
	// text-only: TextX/Y is the scroll offset; TextW/H is the window size;
	// all four are doc-space px. TextMode is "rendered" or "text".
	TextX    int64  `json:"text_x,omitempty"`
	TextY    int64  `json:"text_y,omitempty"`
	TextW    int64  `json:"text_w,omitempty"`
	TextH    int64  `json:"text_h,omitempty"`
	TextMode string `json:"text_mode,omitempty"`
	BlobID   int64  `json:"blob_id,omitempty"`
	// url-only: URLString is the http(s) URL. PreviewBlobID points at
	// the blobs row holding the last-frozen JPEG preview captured at
	// session close; 0 until the first close. The bytes are hash-
	// deduped through the blobs table the same way text content is.
	URLString     string `json:"url_string,omitempty"`
	PreviewBlobID int64  `json:"preview_blob_id,omitempty"`
	// AltText is a human-readable label used as the alt of a markdown
	// link when this tile is dropped into a doc. Populated by the
	// server: URL tiles get the page title (captured on Chromium
	// session close); text tiles get the first non-empty line of
	// content (stripped of markdown markers). Other kinds and tiles
	// with no derived alt fall back to a default at drop time.
	AltText string `json:"alt_text,omitempty"`
	// Reference reports that this well is a LINK, not owned content — its
	// child grid lives in another plugin's id space (a qualified
	// child_grid_id: a mounted plugin, file/process well, or cross-plugin
	// clone). The single authoritative "is a link" signal: the client draws
	// a dashed border from it, and delete/clone already treat a qualified
	// child_grid_id as unlink-only / share (never cascade). Set by the server
	// in qualifyTiles; wire-only, derived, never a stored column.
	Reference bool `json:"reference,omitempty"`
	// ContentZoom scales the content rendered inside a text/shell/url tile
	// (issue #82). Framing: persisted, never bumps version; 0 = unset (1.0).
	ContentZoom float64 `json:"content_zoom,omitempty"`
	// URLHistory is a url tile's persisted navigation back-stack (JSON
	// {index, entries:[{url,title}]}, capped) captured at freeze so a
	// revived tile can still go back (issue #113). "" = none.
	URLHistory string `json:"url_history,omitempty"`
	// LinkTargetID makes a LEAF tile (text/url/shell/pane) a LINK: a
	// qualified "<uuid>/<tile-id>" reference to the tile owning the content.
	// The link row stores no content — readers resolve bytes/preview/session
	// through the target id. The well kind's link variant is a qualified
	// ChildGridID (the exit well); Reference is the one derived "is a link"
	// bit over both shapes. "" = an ordinary owned tile.
	LinkTargetID string `json:"link_target_id,omitempty"`
	// URLFrozen is the user's standing freeze on a url tile (issue #237):
	// set by the explicit freeze gesture, cleared by reconnect. While set,
	// descending does not auto-go-live. Framing — written by the SetTile
	// url_frozen arm only, never bumps version.
	URLFrozen bool `json:"url_frozen,omitempty"`
	// ConfigurePluginID marks a CHILDLESS well as an UNCONFIGURED PLUGIN
	// WELL (issue #251): the uuid of the parameterized plugin whose
	// instance will fill it. First descent opens that plugin's instance
	// picker; adopting sets ChildGridID and the uuid stays as provenance.
	// "" for every other tile.
	ConfigurePluginID string `json:"configure_plugin_id,omitempty"`
	// ServesPage: the owning plugin serves this tile's content as WEB
	// CONTENT through the /content/ door (2026-08-11) — the client gives
	// the descent url-tile semantics at the derived address
	// <origin>/content/<token>/<ContentID>/. Plugin-declared, derived from
	// the content itself; never a stored column.
	ServesPage bool `json:"serves_page,omitempty"`
	// TextPresentation: the plugin's declaration of how a text body
	// presents — TextPresentationPlain (monospace, no markdown
	// interpretation), TextPresentationRendered, or TextPresentationBoth
	// (rendered by default, user may toggle to the raw source). "" = the
	// stored user text_mode rules (localdb docs). Wire-only,
	// plugin-derived.
	TextPresentation string `json:"text_presentation,omitempty"`
	// MenuEntry names the plugin MenuEntry this tile was minted from
	// (issue #258; "" for ordinary tiles). The plugin recognizes its own
	// tools by it; the client prompts for the entry's params on first
	// descent while the tile has no content.
	MenuEntry string `json:"menu_entry,omitempty"`
	// StatusDetail is the owning plugin's current trouble with this tile,
	// displayed verbatim (e.g. an ssh connection well's last dial error
	// while it has no child yet). Wire-only, plugin-derived — never a
	// stored column, never set by clients.
	StatusDetail string `json:"status_detail,omitempty"`
}

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

// Reads.

type GetGridRequest struct {
	GridID string `json:"grid_id"`
}
type GetGridResponse struct {
	Grid  Grid   `json:"grid"`
	Tiles []Tile `json:"tiles"`
}

type GetTilePreviewRequest struct {
	TileID string `json:"tile_id"`
}
type GetTilePreviewResponse struct {
	JPEG []byte `json:"jpeg"`
}

// TileResponse is the common shape returned by tile-producing mutations.
type TileResponse struct {
	Tile Tile `json:"tile"`
}

// Creates: no Version (the tile doesn't exist yet).

// PluginInfo describes one configured plugin for the launcher / + menu.
type PluginInfo struct {
	UUID  string `json:"uuid"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Glyph is the plugin's DECLARED identity glyph (InfoResponse.glyph):
	// "folder", "process", "well", or "" for the generic globe. The client
	// renders from this, never from the kind string (charter, 2026-08-15).
	Glyph string `json:"glyph,omitempty"`
	// MenuEntries: the plugin's declared (+) menu additions (issue #258).
	MenuEntries []MenuEntry `json:"menu_entries,omitempty"`
	Writable    bool        `json:"writable"`
	RootGridID  string      `json:"root_grid_id"` // qualified; click-enter descends here
	// ScratchGridID is the qualified off-grid grid this plugin holds ephemeral
	// url tiles in ("descend into a url"); "" if the plugin has none.
	ScratchGridID string `json:"scratch_grid_id,omitempty"`
	// InstanceGridID is the qualified off-grid grid holding this plugin's
	// parameterized instances (issue #251 — e.g. ssh's connection wells).
	// Set with an empty RootGridID it marks the plugin PARAMETERIZED: the
	// menu click and the drop-then-descend gestures open the instance picker
	// instead of descending. A storage address, never a landing page.
	InstanceGridID string `json:"instance_grid_id,omitempty"`
	// RootViewCx/Cy/Zoom is the plugin root grid's last-saved viewport from
	// the Info handshake (center in world cell coords, live zoom). Zero means
	// "never visited"; the client substitutes the default calibrated zoom.
	// Filled by localdb from its system KV table; zero for fs/proc.
	RootViewCx   float64 `json:"root_view_cx,omitempty"`
	RootViewCy   float64 `json:"root_view_cy,omitempty"`
	RootViewZoom float64 `json:"root_view_zoom,omitempty"`
	// InfoError is set when the plugin's Info handshake failed or timed out —
	// a crashed/hung plugin ("broken"), distinct from a healthy plugin that
	// simply has no root configured ("rootless"), which leaves this empty even
	// though RootGridID is also "". See client/pluginhealth.Classify.
	InfoError string `json:"info_error,omitempty"`
}

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
	// ViewX/ViewY/ViewZoom seed an exit well's framing at creation (e.g. a
	// plugin link dropped from the + menu starts at the plugin's persisted
	// root view, the same framing a node-grid tile shows). Zeros = default
	// view. Ignored for interior wells, which always start unframed.
	ViewX    int64   `json:"view_x,omitempty"`
	ViewY    int64   `json:"view_y,omitempty"`
	ViewZoom float64 `json:"view_zoom,omitempty"`
	// ObjectID carries the source's provenance marker — a cross-plugin LINK
	// to an existing well, or a deep-copied interior well (#200) — "" = mint
	// fresh.
	ObjectID string `json:"object_id,omitempty"`
}

// (CreateWellRequest.ConfigurePluginID is gone — unconfigured plugin
// wells stopped being creatable when the instance picker retired,
// 2026-08-23; Tile.ConfigurePluginID stays for the stale wells that
// still exist in stores.)

// AdoptChildGridRequest is the SetTile adopt arm (issue #251): a versioned
// user edit turning a CHILDLESS well into a link by setting its child grid.
// Label applies only when the well is unnamed (the copy-from-source-at-birth
// naming rule links follow); ViewX/Y/Zoom seed the framing like an exit
// well's birth fields.
type AdoptChildGridRequest struct {
	TileID      string  `json:"tile_id"`
	Version     int64   `json:"version"`
	ChildGridID string  `json:"child_grid_id"`
	Label       string  `json:"label,omitempty"`
	ViewX       int64   `json:"view_x,omitempty"`
	ViewY       int64   `json:"view_y,omitempty"`
	ViewZoom    float64 `json:"view_zoom,omitempty"`
}

// CreateLeafLinkRequest creates a LEAF LINK: a text/url/shell/pane tile whose
// content lives in another plugin's tile (the cross-plugin left-drag). Kind is
// the target's kind; LinkTargetID is the qualified "<uuid>/<tile-id>"
// reference; Label is the link's local alt_text (usually the source's);
// ObjectID carries provenance ("" = fresh).
type CreateLeafLinkRequest struct {
	GridID       string `json:"grid_id"`
	X            int64  `json:"x"`
	Y            int64  `json:"y"`
	W            int64  `json:"w"`
	H            int64  `json:"h"`
	Kind         string `json:"kind"`
	LinkTargetID string `json:"link_target_id"`
	Label        string `json:"label,omitempty"`
	ObjectID     string `json:"object_id,omitempty"`
}

type CreateTextRequest struct {
	GridID string `json:"grid_id"`
	X      int64  `json:"x"`
	Y      int64  `json:"y"`
	W      int64  `json:"w"`
	H      int64  `json:"h"`
	Data   []byte `json:"data"`
	// ObjectID carries the source's provenance marker on a cross-plugin
	// clone ("" = mint fresh). See store createTile.
	ObjectID string `json:"object_id,omitempty"`
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
	// ObjectID carries the source's provenance marker on a cross-plugin
	// clone ("" = mint fresh). See store createTile.
	ObjectID string `json:"object_id,omitempty"`
}

type CreateURLRequest struct {
	GridID string `json:"grid_id"`
	X      int64  `json:"x"`
	Y      int64  `json:"y"`
	W      int64  `json:"w"`
	H      int64  `json:"h"`
	URL    string `json:"url"`
	// ObjectID carries the source's provenance marker on a cross-plugin
	// clone ("" = mint fresh). See store createTile.
	ObjectID string `json:"object_id,omitempty"`
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
	// ObjectID carries the source's provenance marker on a cross-plugin
	// clone ("" = mint fresh). See store createTile.
	ObjectID string `json:"object_id,omitempty"`
}

// Mutations: Version is the claimed current version of TileID.
// Server returns 409 / ErrVersionConflict if it does not match.

type CloneTileRequest struct {
	TileID     string `json:"tile_id"`
	Version    int64  `json:"version"`
	DestGridID string `json:"dest_grid_id"`
	X          int64  `json:"x"`
	Y          int64  `json:"y"`
}

// PlaceTileRequest is the single placement writeback (2026-07-26,
// interface-redesign-plan.md): placement is one fact — (grid, x, y, w, h) —
// and one verb owns it. GridID is the DESTINATION grid (the tile's current
// grid for a pure resize). Id-addressed + version-claimed; no Path — the
// well-into-own-subtree refusal is a server-side ancestor walk.
type PlaceTileRequest struct {
	TileID  string `json:"tile_id"`
	Version int64  `json:"version"`
	GridID  string `json:"grid_id"`
	X       int64  `json:"x"`
	Y       int64  `json:"y"`
	W       int64  `json:"w"`
	H       int64  `json:"h"`
}

type SetWellViewRequest struct {
	TileID   string  `json:"tile_id"`
	Version  int64   `json:"version"`
	ViewX    int64   `json:"view_x"`
	ViewY    int64   `json:"view_y"`
	ViewZoom float64 `json:"view_zoom"`
}

type SetTextViewRequest struct {
	TileID   string `json:"tile_id"`
	Version  int64  `json:"version"`
	TextX    int64  `json:"text_x"`
	TextY    int64  `json:"text_y"`
	TextW    int64  `json:"text_w"`
	TextH    int64  `json:"text_h"`
	TextMode string `json:"text_mode"`
}

// SetShellPreviewRequest stores the JPEG frame captured at ascent as
// the frozen preview. Bytes are hash-deduped through the blobs table.
type SetShellPreviewRequest struct {
	TileID  string `json:"tile_id"`
	Version int64  `json:"version"`
	JPEG    []byte `json:"jpeg"`
}

// ShellSessionAliveRequest asks whether the gridwell-private tmux
// session for tile_id currently exists. The wasm side uses the
// answer to gate the refresh button on descent — see CLAUDE.md /
// the shell-tile design notes for the truth table.
type ShellSessionAliveRequest struct {
	TileID string `json:"tile_id"`
}

// ShellSessionAliveResponse is the answer side of the probe.
type ShellSessionAliveResponse struct {
	Alive bool `json:"alive"`
}

// SetRootViewRequest persists the plugin root-grid framing. RootGridID
// (a qualified "<plugin-uuid>/<id>") routes the call on the wire; the store
// uses only Cx/Cy/Zoom (the qualified prefix has been stripped by the time the
// store sees it). Framing only — never bumps a content version.
type SetRootViewRequest struct {
	RootGridID string  `json:"root_grid_id,omitempty"` // wire routing; stripped at store
	Cx         float64 `json:"cx"`
	Cy         float64 `json:"cy"`
	Zoom       float64 `json:"zoom"`
}

// SetURLStateRequest freezes a live URL tile (preview JPEG + address +
// title) when its Electron WebContentsView is torn down on ascend. The
// Version claim makes the freeze a proper versioned content edit — an in-place
// write to this tile's row (copy-on-clone: clones are independent, so there
// is no fork). Empty jpeg/url/title fields are skipped.
type SetURLStateRequest struct {
	TileID  string `json:"tile_id"`
	Version int64  `json:"version"`
	JPEG    []byte `json:"jpeg"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	// History is the JSON back-stack captured at freeze ("" = leave the
	// stored history untouched — a partial capture must not clobber it).
	History string `json:"history,omitempty"`
}

// SetContentZoomRequest persists a tile's content scale (framing; no bump).
type SetContentZoomRequest struct {
	TileID      string  `json:"tile_id"`
	Version     int64   `json:"version"`
	ContentZoom float64 `json:"content_zoom"`
}

// SetURLFrozenRequest persists the user's standing freeze on a url tile
// (issue #237; framing, no bump).
type SetURLFrozenRequest struct {
	TileID  string `json:"tile_id"`
	Version int64  `json:"version"`
	Frozen  bool   `json:"frozen"`
}

type DeleteTileRequest struct {
	TileID  string `json:"tile_id"`
	Version int64  `json:"version"`
}
type DeleteTileResponse struct{}

// Event stream.

type SubscribeRequest struct{}

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

type GridChanged struct {
	GridID string `json:"grid_id"`
}
type TileChanged struct {
	Tile Tile `json:"tile"`
}
type TileRemoved struct {
	GridID string `json:"grid_id"`
	TileID string `json:"tile_id"`
}

// PluginHealth reports a transition in a plugin's event-stream health (see
// internal/server's fan-in): emitted once on the down transition (Healthy
// false, Detail = the dial/recv/Info error) and once on recovery (Healthy
// true, Detail ""). Not one per retry attempt — only on a change of state.
type PluginHealth struct {
	PluginUUID string `json:"plugin_uuid"`
	Healthy    bool   `json:"healthy"`
	Detail     string `json:"detail,omitempty"`
}
