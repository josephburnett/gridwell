package nav

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/transition"
)

// The effect vocabulary: everything navigation asks the shim to do. It is one
// tagged struct rather than an interface per effect, so a plan is comparable
// field by field in a table test. Each kind names the fields it reads. There
// is no Redraw, because the executor draws once after every plan.
type EffectKind int

const (
	// EffInstallPlace installs a pane's place: PaneID, Stack, Viewport (nil
	// keeps the pane's own).
	EffInstallPlace   EffectKind = iota
	EffClearSelection            // PaneID
	// EffRelocatePane moves a pane onto another and descends it into a tile:
	// PaneID, DestPaneID, TileID, Foot, Zoom.
	EffRelocatePane
	EffForgetPane // drops every session resource keyed to a pane: PaneID
	// EffInstallLevel swaps the whole pane tree for a pane-tile level: Level,
	// Tree (nil with Capture set: install the window layout as it stands at
	// the swap), Baseline, KeepOuter, IDPrefix.
	EffInstallLevel
	// EffPopLevel leaves one pane-tile level, restoring the tree it parked or
	// a fresh pane at GridID: GridID.
	EffPopLevel

	// EffFlushFraming persists every pane's settled grid framing now; the
	// pane.FramingWriters rule stays in the executor.
	EffFlushFraming
	// EffPersistFraming writes one pane's framing onto the row that owns it:
	// PaneID, Owner, Door (true for the doorway arm).
	EffPersistFraming
	EffSaveText            // posts an editor buffer and framed window: PaneID, TileID
	EffFlushDirtyText      // flushes every unsaved edit now
	EffFlushLayout         // persists the pane-tile layout blob now
	EffFlushDroppedSubtree // flushes the writebacks a closing subtree owes

	EffCancelTransition // lands what a pane is animating: PaneID ("" = all)
	// EffStartTransition animates a pane: PaneID, Segments, TraceTileID, Land
	// (resumed from OnComplete; a cancelled transition still lands). Expand,
	// with Tile, asks for the pane-tile capture animation instead: the tile's
	// face growing into the level outline, the content never moving.
	EffStartTransition

	EffCloseStream // PaneID, Streams, Freeze, FreezeOnto
	EffOpenStream  // PaneID, TileID, Stream
	// EffPlaceURLView re-parents a live url view onto a pane: PaneID, TileID,
	// Tile (by value: a just-created tile is in no cached grid).
	EffPlaceURLView
	EffRefreshOverlay // re-syncs the text and rendered overlays
	// EffScaleContent re-derives a pane's content render scale from its rect
	// and the tile's intrinsic zoom: PaneID.
	EffScaleContent

	// EffFetchGrid warms a grid: GridID, or PaneID for the grid this pane's
	// place names, which only the executor can resolve.
	EffFetchGrid
	EffFetchTileContent // warms a tile's body: TileID
	EffDropTileContent  // drops a cached body before refetching it: ContentID
	EffAwait            // starts an async read to resume with: Token, Request

	EffOpenMenu // reopens the + menu and clears the frame flag: PaneID
	EffCloseMenu
	EffScheduleURLUpdate // arms the debounced history writer
	EffWriteURLNow       // writes the history entry now
	EffPlaceCursor       // puts the text cursor at a position: Col, Row

	EffDeleteEphemeral // deletes an ascended-from visit: GridID, TileID
	EffReport          // surfaces a notice: Severity, Source, Message
	// EffEnterLevel descends the window into a pane tile: PaneID, TileID, Tile
	// (by value, as GestureDescend's Door is). It re-enters the machine
	// against a world gathered after the effects above it.
	EffEnterLevel
	EffLeaveLevels // leaves pane-tile levels: Count. Re-enters the same way.
	EffReEngage    // PaneID, TileID. Re-enters the same way.
)

// StreamKind names one live surface class.
type StreamKind int

const (
	StreamURL   StreamKind = iota // the native url view
	StreamShell                   // the PTY attachment
	// StreamBoth is the teardown for a content frame whose row is gone,
	// where nothing says which kind it was.
	StreamBoth
)

// FreezeTarget names the row a closing url view's capture is persisted onto
// when it is not the row the view was opened for.
type FreezeTarget struct {
	TileID string
	GridID string
}

// Viewport is a pane's centre and zoom in the grid it shows.
type Viewport struct{ Cx, Cy, Zoom float64 }

// Effect is one thing the shim does.
type Effect struct {
	Kind EffectKind

	PaneID     string
	DestPaneID string
	TileID     string
	GridID     string
	ContentID  string
	// Tile is a row by value, for effects that must act on the row the
	// gesture read: an ephemeral scratch tile is in no cached grid.
	Tile *gridwellv1.Tile

	// Place and tree.
	Stack     *pane.Stack
	Viewport  *Viewport
	Foot      pane.Footprint
	Zoom      float64
	Level     *pane.Level
	Tree      *pane.Tree
	Baseline  []byte
	KeepOuter bool
	Capture   bool
	IDPrefix  string
	Count     int

	// Writeback.
	Owner pane.FramingOwner
	Door  bool

	// Transition.
	Segments    []transition.Segment
	TraceTileID string
	Land        Token
	Expand      bool

	// Surface.
	Streams    StreamKind
	Stream     StreamKind
	Freeze     bool
	FreezeOnto *FreezeTarget

	// Await.
	Token   Token
	Request Request

	// Feedback.
	Severity errsurface.Severity
	Source   string
	Message  string

	// Cursor.
	Col, Row int
}

// RequestKind is the closed set of async reads the machine starts.
type RequestKind int

const (
	// RequestGetTile reads one tile row: ID. The executor caches the answer
	// before the resume, so the machine and the renderer act on one row.
	RequestGetTile RequestKind = iota
	// RequestGetGrid reads one grid: ID. The walk asks for a grid at most
	// once, however it answered.
	RequestGetGrid
	// RequestReadContent reads a tile's body into the cache: ID.
	RequestReadContent
	// RequestReadLayout reads a pane tile's layout blob back as Data: ID. It
	// is separate from RequestReadContent because a layout never seeds the
	// text overlay and is decoded here rather than cached as a body.
	RequestReadLayout
	// RequestSearch locates a tile: Query, Scope, Limit.
	RequestSearch
	// RequestProbeShell asks whether a shell session is alive: ID is the
	// content id the shell facts key by.
	RequestProbeShell
)

// Request is one async read.
type Request struct {
	Kind  RequestKind
	ID    string
	Query string
	Scope string
	Limit int
}

// Result is one async answer, handed back to Resume with the token that
// asked for it.
type Result struct {
	// OK is false when the read failed or answered nothing usable: a search
	// with no hit is the same no as a search that could not run.
	OK bool
	// Alive answers RequestProbeShell.
	Alive bool
	// Tile answers RequestGetTile by value, so the step acts on the row that
	// was read.
	Tile *gridwellv1.Tile
	// Wells answers RequestSearch: the hit's containing-well chain from its
	// root, outermost first. Empty means the tile sits at a root.
	Wells []*gridwellv1.Tile
	// Data answers RequestReadLayout: the bytes, which the machine decodes
	// itself, since client/pane's codec is pure.
	Data []byte
	// Err is a failed read's text, stripped of the wire prefix. A step that
	// surfaces its failure plans the notice with it.
	Err string
}
