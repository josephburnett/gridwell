// Package rpc declares the wire-format types for the Gridwell RPC service.
//
// The server transports them as JSON over HTTP under /rpc/<MethodName>; the
// client uses the same encoding.
package rpc

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
}

// Reads.

type GetGridRequest struct {
	GridID string `json:"grid_id"`
}
type GetGridResponse struct {
	Grid  Grid   `json:"grid"`
	Tiles []Tile `json:"tiles"`
}

type GetBlobRequest struct {
	BlobID int64 `json:"blob_id"`
}
type GetBlobResponse struct {
	Data []byte `json:"data"`
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
	X      int64 `json:"x"`
	Y      int64 `json:"y"`
	W      int64 `json:"w"`
	H      int64 `json:"h"`
}

// Mutations: Version is the claimed current version of TileID.
// Server returns 409 / ErrVersionConflict if it does not match.

type MoveTileRequest struct {
	Path       Path   `json:"path"`
	TileID     string `json:"tile_id"`
	Version    int64  `json:"version"`
	DestGridID string `json:"dest_grid_id"`
	DestPath   Path  `json:"dest_path"`
	X          int64 `json:"x"`
	Y          int64 `json:"y"`
}

type CloneTileRequest struct {
	Path       Path   `json:"path"`
	TileID     string `json:"tile_id"`
	Version    int64  `json:"version"`
	DestGridID string `json:"dest_grid_id"`
	DestPath   Path  `json:"dest_path"`
	X          int64 `json:"x"`
	Y          int64 `json:"y"`
}

type ResizeTileRequest struct {
	Path    Path   `json:"path"`
	TileID  string `json:"tile_id"`
	Version int64 `json:"version"`
	X       int64 `json:"x"`
	Y       int64 `json:"y"`
	W       int64 `json:"w"`
	H       int64 `json:"h"`
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

// SetRootViewRequest is the localdb store's root-framing setter (not on the
// wire — the rootless model has no app root; kept as internal store API).
type SetRootViewRequest struct {
	Cx   float64 `json:"cx"`
	Cy   float64 `json:"cy"`
	Zoom float64 `json:"zoom"`
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
	Version int64 `json:"version"`
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
