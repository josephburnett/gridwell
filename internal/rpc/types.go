// Package rpc declares the wire-format types for the Gridwell RPC service.
//
// The server transports them as JSON over HTTP under /rpc/<MethodName>; the
// client uses the same encoding.
package rpc

import "strings"

// Tile kinds. A tile is exactly one of these.
//
// The "interior" kinds — well, text, url — live inside Gridwell. file-well,
// process-well, and shell are "exit" kinds: their contents reflect state
// owned by the host (the filesystem, the process table, a bash session),
// not by Gridwell. The color grammar (red outline) follows.
const (
	// Canonical alt-text strings supplied by the fs/proc plugins as the
	// Attach label of a file/process well. The client renders tile.AltText
	// verbatim — no derivation — so server and client always agree on what a
	// tile is called.
	AltFiles     = "files"     // root file well (path "/")
	AltProcesses = "processes" // root process well (PID 1)
	AltInfo      = "info"      // synthetic @info tile inside a process grid

	KindWell  = "well"
	KindText  = "text"
	KindURL   = "url"
	KindShell = "shell"
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
// plugin-scoped local id prefixed with the owning plugin's UUID. QualifyID and
// UUIDOf are its only encode/decode points — shared by the server (id
// qualification, routing.go) and the client (cache lookup, exit-well
// classification) so the two can never disagree on the format.

// QualifyID builds a qualified id "<uuid>/<local>" from its parts.
func QualifyID(uuid, local string) string { return uuid + "/" + local }

// UUIDOf returns the plugin-uuid segment of a qualified id ("<uuid>/<local>"),
// or "" when the id has no "/" (a bare/unqualified id). Inverse of QualifyID on
// the uuid segment.
func UUIDOf(id string) string {
	if i := strings.IndexByte(id, '/'); i >= 0 {
		return id[:i]
	}
	return ""
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
	}
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

// Grid source kinds. NULL ("") means a regular Gridwell-owned grid. fs
// means the grid's tile list is reconciled against a host directory; proc
// means against the host process table. SourceID carries the path or PID.
const (
	GridSourceFS   = "fs"
	GridSourceProc = "proc"
)

// Text-tile display modes.
const (
	TextModeRendered = "rendered"
	TextModeText     = "text"
)

// Path is the sequence of well-tile IDs walked from the root grid to the
// pane the request originates from. Mutations carry it so the store can
// validate the editing pane really sits in this leaf grid (checkPathLeaf);
// copy-on-clone keeps tiles unshared, so the edit writes in place — no fork.
type Path struct {
	WellIDs []string `json:"well_ids"`
}

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

// MountRequest mounts a plugin (by uuid, default config) as an exit well in
// the destination grid.
type MountRequest struct {
	PluginUUID string `json:"plugin_uuid"`
	Path       Path   `json:"path"`
	GridID     string `json:"grid_id"`
	X          int64  `json:"x"`
	Y          int64  `json:"y"`
	W          int64  `json:"w"`
	H          int64  `json:"h"`
}

// PluginInfo describes one configured plugin for the launcher / + menu.
type PluginInfo struct {
	UUID       string `json:"uuid"`
	Kind       string `json:"kind"`
	Label      string `json:"label"`
	Writable   bool   `json:"writable"`
	RootGridID string `json:"root_grid_id"` // qualified; click-enter descends here
	// ScratchGridID is the qualified off-grid grid this plugin holds ephemeral
	// url tiles in ("descend into a url"); "" if the plugin has none.
	ScratchGridID string `json:"scratch_grid_id,omitempty"`
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
	Path   Path   `json:"path"`
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
}

type CreateTextRequest struct {
	Path   Path   `json:"path"`
	GridID string `json:"grid_id"`
	X      int64  `json:"x"`
	Y      int64  `json:"y"`
	W      int64  `json:"w"`
	H      int64  `json:"h"`
	Data   []byte `json:"data"`
}

type CreateURLRequest struct {
	Path   Path   `json:"path"`
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
	Path   Path   `json:"path"`
	GridID string `json:"grid_id"`
	X      int64  `json:"x"`
	Y      int64  `json:"y"`
	W      int64  `json:"w"`
	H      int64  `json:"h"`
}

// Mutations: Version is the claimed current version of TileID.
// Server returns 409 / ErrVersionConflict if it does not match.

type MoveTileRequest struct {
	Path       Path   `json:"path"`
	TileID     string `json:"tile_id"`
	Version    int64  `json:"version"`
	DestGridID string `json:"dest_grid_id"`
	DestPath   Path   `json:"dest_path"`
	X          int64  `json:"x"`
	Y          int64  `json:"y"`
}

type CloneTileRequest struct {
	Path       Path   `json:"path"`
	TileID     string `json:"tile_id"`
	Version    int64  `json:"version"`
	DestGridID string `json:"dest_grid_id"`
	DestPath   Path   `json:"dest_path"`
	X          int64  `json:"x"`
	Y          int64  `json:"y"`
}

type ResizeTileRequest struct {
	Path    Path   `json:"path"`
	TileID  string `json:"tile_id"`
	Version int64  `json:"version"`
	X       int64  `json:"x"`
	Y       int64  `json:"y"`
	W       int64  `json:"w"`
	H       int64  `json:"h"`
}

type SetWellViewRequest struct {
	Path     Path    `json:"path"`
	TileID   string  `json:"tile_id"`
	Version  int64   `json:"version"`
	ViewX    int64   `json:"view_x"`
	ViewY    int64   `json:"view_y"`
	ViewZoom float64 `json:"view_zoom"`
}

type SetTextViewRequest struct {
	Path     Path   `json:"path"`
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
	Path    Path   `json:"path"`
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
// title) when its Electron WebContentsView is torn down on ascend. Path +
// Version make the freeze a proper versioned content edit — an in-place
// write to this tile's row (copy-on-clone: clones are independent, so there
// is no fork). Empty jpeg/url/title fields are skipped.
type SetURLStateRequest struct {
	Path    Path   `json:"path"`
	TileID  string `json:"tile_id"`
	Version int64  `json:"version"`
	JPEG    []byte `json:"jpeg"`
	URL     string `json:"url"`
	Title   string `json:"title"`
}

type UpdateTextRequest struct {
	Path    Path   `json:"path"`
	TileID  string `json:"tile_id"`
	Version int64  `json:"version"`
	Data    []byte `json:"data"`
}

type DeleteTileRequest struct {
	Path    Path   `json:"path"`
	TileID  string `json:"tile_id"`
	Version int64  `json:"version"`
}
type DeleteTileResponse struct{}

// Event stream.

type SubscribeRequest struct{}

type EventKind string

const (
	EventGridChanged EventKind = "grid_changed"
	EventTileChanged EventKind = "tile_changed"
	EventTileRemoved EventKind = "tile_removed"
)

type Event struct {
	Kind        EventKind    `json:"kind"`
	GridChanged *GridChanged `json:"grid_changed,omitempty"`
	TileChanged *TileChanged `json:"tile_changed,omitempty"`
	TileRemoved *TileRemoved `json:"tile_removed,omitempty"`
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
