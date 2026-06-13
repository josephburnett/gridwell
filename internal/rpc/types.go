// Package rpc declares the wire-format types for the Gridwell RPC service.
//
// The server transports them as JSON over HTTP under /rpc/<MethodName>; the
// client uses the same encoding.
package rpc

// Tile kinds. A tile is exactly one of these.
//
// The four "interior" kinds — well, text, url, blackhole — live inside
// Gridwell. file-well and process-well are "exit" kinds: their child
// grids reflect state owned by the host (the filesystem and the process
// table), not by Gridwell. The color grammar (red outline) follows.
const (
	// Canonical alt-text strings stamped into a tile's alt_text at insert
	// time. The client renders tile.AltText verbatim — no derivation —
	// so server and client always agree on what a tile is called.
	AltNull      = "/dev/null" // blackhole tiles
	AltFiles     = "files"     // root file-well (FSPath == "/")
	AltProcesses = "processes" // root process-well (PID == 1)
	AltInfo      = "info"      // synthetic @info tile inside a proc-well

	KindWell        = "well"
	KindText        = "text"
	KindURL         = "url"
	KindBlackHole   = "blackhole"
	KindFileWell    = "file-well"
	KindProcessWell = "process-well"
	KindShell       = "shell"
)

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
// pane the request originates from. Mutations carry it so the store can fork
// the COW spine of shared grids up to the highest one still uniquely owned.
type Path struct {
	WellIDs []int64 `json:"well_ids"`
}

// Grid is the persistent unit of canvas. Tiles live in grids; wells point at
// child grids. The root grid has no parent.
//
// SourceKind is "" for a regular Gridwell-owned grid, "fs" for a grid whose
// tile list is reconciled against a host directory, "proc" for the process
// table. SourceID carries the path or PID; clients use SourceKind to pick
// the red color theme on descent.
type Grid struct {
	ID         int64  `json:"id"`
	ObjectID   string `json:"object_id"`
	Version    int64  `json:"version"`
	SourceKind string `json:"source_kind,omitempty"`
	SourceID   string `json:"source_id,omitempty"`
}

// Tile is the persistent unit of content in a grid. Kind selects which subset
// of the optional fields is meaningful.
type Tile struct {
	ID       int64  `json:"id"`
	ObjectID string `json:"object_id"`
	Version  int64  `json:"version"`
	GridID   int64  `json:"grid_id"`
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
	ChildGridID int64   `json:"child_grid_id,omitempty"`
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
	// file-well-only: the absolute host path the well points at.
	FSPath string `json:"fs_path,omitempty"`
	// SourceKey is the stable per-source identifier used by the
	// source-grid reconciler to dedup tiles against their backing
	// host artifact: for fs-grids, the basename of the file or
	// directory; for proc-grids, the PID as a decimal string ("@info"
	// for the synthetic well-self tile). Empty for tiles in regular
	// Gridwell-owned grids.
	SourceKey string `json:"source_key,omitempty"`
	// process-well-only: the PID the well points at.
	PID int64 `json:"pid,omitempty"`
	// AltText is a human-readable label used as the alt of a markdown
	// link when this tile is dropped into a doc. Populated by the
	// server: URL tiles get the page title (captured on Chromium
	// session close); text tiles get the first non-empty line of
	// content (stripped of markdown markers). Other kinds and tiles
	// with no derived alt fall back to a default at drop time.
	AltText string `json:"alt_text,omitempty"`
}

// Bootstrap RPC: client asks for the current root grid id and root framing.

type BootstrapRequest struct{}
type BootstrapResponse struct {
	RootGridID int64   `json:"root_grid_id"`
	RootViewCx float64 `json:"root_view_cx"`
	RootViewCy float64 `json:"root_view_cy"`
	RootZoom   float64 `json:"root_zoom"`
}

// Reads.

type GetGridRequest struct {
	GridID int64 `json:"grid_id"`
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
	TileID int64 `json:"tile_id"`
}
type GetTilePreviewResponse struct {
	JPEG []byte `json:"jpeg"`
}

// TileResponse is the common shape returned by tile-producing mutations.
type TileResponse struct {
	Tile Tile `json:"tile"`
}

// Creates: no Version (the tile doesn't exist yet).

type CreateWellRequest struct {
	Path   Path  `json:"path"`
	GridID int64 `json:"grid_id"`
	X      int64 `json:"x"`
	Y      int64 `json:"y"`
	W      int64 `json:"w"`
	H      int64 `json:"h"`
}

type CreateTextRequest struct {
	Path   Path   `json:"path"`
	GridID int64  `json:"grid_id"`
	X      int64  `json:"x"`
	Y      int64  `json:"y"`
	W      int64  `json:"w"`
	H      int64  `json:"h"`
	Data   []byte `json:"data"`
}

type CreateURLRequest struct {
	Path   Path   `json:"path"`
	GridID int64  `json:"grid_id"`
	X      int64  `json:"x"`
	Y      int64  `json:"y"`
	W      int64  `json:"w"`
	H      int64  `json:"h"`
	URL    string `json:"url"`
}

type CreateBlackHoleRequest struct {
	Path   Path  `json:"path"`
	GridID int64 `json:"grid_id"`
	X      int64 `json:"x"`
	Y      int64 `json:"y"`
	W      int64 `json:"w"`
	H      int64 `json:"h"`
}

// CreateFileWellRequest creates a file-well at the given FSPath. The
// canonical starting path from the palette is "/". The well's child grid
// is the singleton fs-grid for FSPath: two file-wells at the same path
// share the same backing grid (FS state is the source of truth, so
// per-clone divergence is impossible).
type CreateFileWellRequest struct {
	Path   Path   `json:"path"`
	GridID int64  `json:"grid_id"`
	X      int64  `json:"x"`
	Y      int64  `json:"y"`
	W      int64  `json:"w"`
	H      int64  `json:"h"`
	FSPath string `json:"fs_path"`
}

// CreateProcessWellRequest creates a process-well at the given PID. The
// canonical starting PID from the palette is 1 (init).
type CreateProcessWellRequest struct {
	Path   Path  `json:"path"`
	GridID int64 `json:"grid_id"`
	X      int64 `json:"x"`
	Y      int64 `json:"y"`
	W      int64 `json:"w"`
	H      int64 `json:"h"`
	PID    int64 `json:"pid"`
}

// CreateShellRequest creates a shell tile. The bash session is not
// started until the user refreshes (matches the URL tile model — drop
// + descend show the frozen preview placeholder until explicitly
// activated). Once activated, the bash lives in a gridwell-private
// tmux session keyed by tile id and persists across ascents until
// the tile is deleted (or the machine reboots).
type CreateShellRequest struct {
	Path   Path  `json:"path"`
	GridID int64 `json:"grid_id"`
	X      int64 `json:"x"`
	Y      int64 `json:"y"`
	W      int64 `json:"w"`
	H      int64 `json:"h"`
}

// Mutations: Version is the claimed current version of TileID.
// Server returns 409 / ErrVersionConflict if it does not match.

type MoveTileRequest struct {
	Path       Path  `json:"path"`
	TileID     int64 `json:"tile_id"`
	Version    int64 `json:"version"`
	DestGridID int64 `json:"dest_grid_id"`
	DestPath   Path  `json:"dest_path"`
	X          int64 `json:"x"`
	Y          int64 `json:"y"`
}

type CloneTileRequest struct {
	Path       Path  `json:"path"`
	TileID     int64 `json:"tile_id"`
	Version    int64 `json:"version"`
	DestGridID int64 `json:"dest_grid_id"`
	DestPath   Path  `json:"dest_path"`
	X          int64 `json:"x"`
	Y          int64 `json:"y"`
}

type ResizeTileRequest struct {
	Path    Path  `json:"path"`
	TileID  int64 `json:"tile_id"`
	Version int64 `json:"version"`
	X       int64 `json:"x"`
	Y       int64 `json:"y"`
	W       int64 `json:"w"`
	H       int64 `json:"h"`
}

type SetWellViewRequest struct {
	Path     Path    `json:"path"`
	TileID   int64   `json:"tile_id"`
	Version  int64   `json:"version"`
	ViewX    int64   `json:"view_x"`
	ViewY    int64   `json:"view_y"`
	ViewZoom float64 `json:"view_zoom"`
}

type SetTextViewRequest struct {
	Path     Path   `json:"path"`
	TileID   int64  `json:"tile_id"`
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
	TileID  int64  `json:"tile_id"`
	Version int64  `json:"version"`
	JPEG    []byte `json:"jpeg"`
}

// ShellSessionAliveRequest asks whether the gridwell-private tmux
// session for tile_id currently exists. The wasm side uses the
// answer to gate the refresh button on descent — see CLAUDE.md /
// the shell-tile design notes for the truth table.
type ShellSessionAliveRequest struct {
	TileID int64 `json:"tile_id"`
}

// ShellSessionAliveResponse is the answer side of the probe.
type ShellSessionAliveResponse struct {
	Alive bool `json:"alive"`
}

type SetRootViewRequest struct {
	Cx   float64 `json:"cx"`
	Cy   float64 `json:"cy"`
	Zoom float64 `json:"zoom"`
}
type SetRootViewResponse struct{}

// SetURLStateRequest freezes a live URL tile (preview JPEG + address +
// title) when its Electron WebContentsView is torn down on ascend. Path +
// Version make the freeze a proper content edit: a shared (cloned) grid is
// forked so the write lands in this clone's row, not every clone's. Empty
// jpeg/url/title fields are skipped.
type SetURLStateRequest struct {
	Path    Path   `json:"path"`
	TileID  int64  `json:"tile_id"`
	Version int64  `json:"version"`
	JPEG    []byte `json:"jpeg"`
	URL     string `json:"url"`
	Title   string `json:"title"`
}

type UpdateTextRequest struct {
	Path    Path   `json:"path"`
	TileID  int64  `json:"tile_id"`
	Version int64  `json:"version"`
	Data    []byte `json:"data"`
}

type DeleteTileRequest struct {
	Path    Path  `json:"path"`
	TileID  int64 `json:"tile_id"`
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
	EventGridForked  EventKind = "grid_forked"
)

type Event struct {
	Kind        EventKind    `json:"kind"`
	GridChanged *GridChanged `json:"grid_changed,omitempty"`
	TileChanged *TileChanged `json:"tile_changed,omitempty"`
	TileRemoved *TileRemoved `json:"tile_removed,omitempty"`
	GridForked  *GridForked  `json:"grid_forked,omitempty"`
}

type GridChanged struct {
	GridID int64 `json:"grid_id"`
}
type TileChanged struct {
	Tile Tile `json:"tile"`
}
type TileRemoved struct {
	GridID int64 `json:"grid_id"`
	TileID int64 `json:"tile_id"`
}
type GridForked struct {
	WellID    int64 `json:"well_id"`
	OldGridID int64 `json:"old_grid_id"`
	NewGridID int64 `json:"new_grid_id"`
}
