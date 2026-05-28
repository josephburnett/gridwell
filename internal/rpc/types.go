// Package rpc declares the wire-format types for the Gridwell RPC service.
//
// The server transports them as JSON over HTTP under /rpc/<MethodName>; the
// client uses the same encoding.
package rpc

// ViewRect is the framed region of the originating pane in the affected grid's
// own coordinates.
type ViewRect struct {
	X int64 `json:"x"`
	Y int64 `json:"y"`
	W int64 `json:"w"`
	H int64 `json:"h"`
}

func (r ViewRect) Contains(x, y, w, h int64) bool {
	return x >= r.X && y >= r.Y && x+w <= r.X+r.W && y+h <= r.Y+r.H
}

func (r ViewRect) Intersects(x, y, w, h int64) bool {
	if w <= 0 || h <= 0 || r.W <= 0 || r.H <= 0 {
		return false
	}
	return x < r.X+r.W && x+w > r.X && y < r.Y+r.H && y+h > r.Y
}

// Path is the sequence of well-tile IDs walked from the root grid to the
// pane the request originates from.
type Path struct {
	WellIDs []int64 `json:"well_ids"`
}

// Grid is the persistent unit of canvas. Tiles live in grids; wells point at
// child grids. The root grid has no parent.
type Grid struct {
	ID            int64   `json:"id"`
	ObjectID      string  `json:"object_id"`
	DefaultViewCx float64 `json:"default_view_cx"`
	DefaultViewCy float64 `json:"default_view_cy"`
	DefaultZoom   float64 `json:"default_zoom"`
}

// Tile is the persistent unit of content in a grid.
type Tile struct {
	ID          int64   `json:"id"`
	ObjectID    string  `json:"object_id"`
	GridID      int64   `json:"grid_id"`
	Type        string  `json:"type"`
	X           int64   `json:"x"`
	Y           int64   `json:"y"`
	W           int64   `json:"w"`
	H           int64   `json:"h"`
	ViewX       int64   `json:"view_x"`
	ViewY       int64   `json:"view_y"`
	ViewZoom    float64 `json:"view_zoom"`
	ViewW       int64   `json:"view_w,omitempty"`
	ViewH       int64   `json:"view_h,omitempty"`
	FileMode    string  `json:"file_mode,omitempty"`
	ChildGridID int64   `json:"child_grid_id,omitempty"`
	Capped      bool    `json:"capped,omitempty"`
	MimeType    string  `json:"mime_type,omitempty"`
	BlobID      int64   `json:"blob_id,omitempty"`
	URLString   string  `json:"url_string,omitempty"`
}

func (t *Tile) IsURL() bool       { return t.Type == "file" && t.MimeType == MimeURIList }
func (t *Tile) IsBlackHole() bool { return t.Type == "file" && t.MimeType == MimeBlackHole }

const MimeURIList = "text/uri-list"
const MimeBlackHole = "application/x-gridwell-blackhole"

// Bootstrap RPC: client asks for the current root grid id.

type BootstrapRequest struct{}
type BootstrapResponse struct {
	RootGridID int64 `json:"root_grid_id"`
}

// Reads.

type GetGridRequest struct {
	GridID int64 `json:"grid_id"`
}
type GetGridResponse struct {
	Grid     Grid   `json:"grid"`
	Tiles    []Tile `json:"tiles"`
	Readable bool   `json:"readable"`
	Writable bool   `json:"writable"`
}

type GetBlobRequest struct {
	BlobID int64 `json:"blob_id"`
}
type GetBlobResponse struct {
	Data     []byte `json:"data"`
	MimeType string `json:"mime_type"`
}

type GetTilePreviewRequest struct {
	TileID int64 `json:"tile_id"`
}
type GetTilePreviewResponse struct {
	JPEG []byte `json:"jpeg"`
}

// Mutations on a grid.

type CreateWellRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	GridID   int64    `json:"grid_id"`
	X        int64    `json:"x"`
	Y        int64    `json:"y"`
	W        int64    `json:"w"`
	H        int64    `json:"h"`
}

type CreateFileRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	GridID   int64    `json:"grid_id"`
	X        int64    `json:"x"`
	Y        int64    `json:"y"`
	W        int64    `json:"w"`
	H        int64    `json:"h"`
	MimeType string   `json:"mime_type"`
	Data     []byte   `json:"data"`
}

type TileResponse struct {
	Tile Tile `json:"tile"`
}

type MoveTileRequest struct {
	Path         Path     `json:"path"`
	ViewRect     ViewRect `json:"view_rect"`
	TileID       int64    `json:"tile_id"`
	DestGridID   int64    `json:"dest_grid_id"`
	DestPath     Path     `json:"dest_path"`
	DestViewRect ViewRect `json:"dest_view_rect"`
	X            int64    `json:"x"`
	Y            int64    `json:"y"`
}
type MoveTileResponse struct {
	Tile Tile `json:"tile"`
}

type CloneTileRequest struct {
	Path         Path     `json:"path"`
	ViewRect     ViewRect `json:"view_rect"`
	TileID       int64    `json:"tile_id"`
	DestGridID   int64    `json:"dest_grid_id"`
	DestPath     Path     `json:"dest_path"`
	DestViewRect ViewRect `json:"dest_view_rect"`
	X            int64    `json:"x"`
	Y            int64    `json:"y"`
}

type ResizeTileRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	TileID   int64    `json:"tile_id"`
	X        int64    `json:"x"`
	Y        int64    `json:"y"`
	W        int64    `json:"w"`
	H        int64    `json:"h"`
}

type SetGridDefaultViewRequest struct {
	GridID int64   `json:"grid_id"`
	Cx     float64 `json:"cx"`
	Cy     float64 `json:"cy"`
	Zoom   float64 `json:"zoom"`
}
type SetGridDefaultViewResponse struct {
	Grid Grid `json:"grid"`
}

type SetTileViewportRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	TileID   int64    `json:"tile_id"`
	ViewX    int64    `json:"view_x"`
	ViewY    int64    `json:"view_y"`
	ViewZoom float64  `json:"view_zoom"`
	// Text-file window size (doc px) + mode, persisted so the parent
	// preview can mirror the last-framed view. Zero/empty leaves the
	// stored values unchanged.
	ViewW    int64  `json:"view_w,omitempty"`
	ViewH    int64  `json:"view_h,omitempty"`
	FileMode string `json:"file_mode,omitempty"`
}

type UpdateFileContentRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	TileID   int64    `json:"tile_id"`
	Data     []byte   `json:"data"`
}

type DeleteTileRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	TileID   int64    `json:"tile_id"`
}
type DeleteTileResponse struct{}

type AscendAtRootRequest struct{}
type AscendAtRootResponse struct {
	NewRootGridID int64 `json:"new_root_grid_id"`
	WellID        int64 `json:"well_id"`
}

type ForkURLRequest struct {
	Path         Path     `json:"path"`
	ViewRect     ViewRect `json:"view_rect"`
	TileID       int64    `json:"tile_id"`
	DestGridID   int64    `json:"dest_grid_id"`
	DestPath     Path     `json:"dest_path"`
	DestViewRect ViewRect `json:"dest_view_rect"`
	X            int64    `json:"x"`
	Y            int64    `json:"y"`
}
type ForkURLResponse struct {
	Tile Tile `json:"tile"`
}

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
