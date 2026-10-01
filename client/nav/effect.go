package nav

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/transition"
)

// EffectKind is everything navigation asks the shim to do; each kind names the
// fields it reads. There is no Redraw: the executor draws after every plan.
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
	// EffInstallLevel swaps the pane tree for a pane-tile level: Level, Tree
	// (nil with Capture: the window layout at the swap), Baseline, KeepOuter,
	// IDPrefix.
	EffInstallLevel
	// EffPopLevel leaves one pane-tile level, restoring the tree it parked or
	// a fresh pane at GridID: GridID.
	EffPopLevel

	// EffFlushFraming persists every pane's settled grid framing now.
	EffFlushFraming
	// EffPersistFraming writes one pane's framing onto the row that owns it:
	// PaneID, Owner, Door (true for the doorway arm).
	EffPersistFraming
	EffSaveText            // posts an editor buffer and framed window: PaneID, TileID
	EffFlushDirtyText      // flushes every unsaved edit now
	EffFlushLayout         // persists the pane-tile layout blob now
	EffFlushDroppedSubtree // flushes the writebacks a closing subtree owes
	// EffHandBackSurfaces moves each live surface of the level being left to
	// its pane.Heir in the parked tree.
	EffHandBackSurfaces

	EffCancelTransition // lands what a pane is animating: PaneID ("" = all)
	// EffStartTransition animates a pane: PaneID, Segments, TraceTileID, Land.
	// Expand, with Tile, grows the tile's face into the level outline instead.
	EffStartTransition

	EffCloseStream // PaneID, Streams, Freeze, FreezeOnto
	EffOpenStream  // PaneID, TileID, Stream
	// EffPlaceURLView re-parents a live url view onto a pane: PaneID, TileID,
	// Tile (by value: a just-created tile is in no cached grid).
	EffPlaceURLView
	EffRefreshOverlay // re-syncs the text and rendered overlays
	// EffScaleContent re-derives a pane's content render scale: PaneID.
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

	EffDeleteEphemeral // deletes an ended visit: GridID, TileID; see stepRetireVisit
	EffReport          // surfaces a notice: Severity, Source, Message
	// EffEnterLevel descends the window into a pane tile: PaneID, TileID, Tile.
	// It re-enters the machine against a world gathered after the effects
	// above it.
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
	// Tile is by value: an ephemeral scratch tile is in no cached grid.
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

// RequestKind is the closed set of async asks the machine starts.
type RequestKind int

const (
	// RequestGetTile reads one tile row: ID. The executor caches the answer
	// before the resume, so the machine and the renderer act on one row.
	RequestGetTile     RequestKind = iota
	RequestGetGrid                 // ID
	RequestReadContent             // reads a tile's body into the cache: ID
	// RequestReadLayout reads a pane tile's layout blob back as Data, never
	// into the cache: ID.
	RequestReadLayout
	RequestSearch     // Query, Scope, Limit
	RequestProbeShell // ID is the content id the shell facts key by
	// RequestFlushLayout writes the pane-tile layout as the tree stands now
	// and answers OK once the node holds it.
	RequestFlushLayout
)

// Request is one async ask.
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
	OK    bool
	Alive bool
	Tile  *gridwellv1.Tile
	// Wells is the search hit's containing-well chain, outermost first; empty
	// means the tile sits at a root.
	Wells []*gridwellv1.Tile
	Data  []byte
	// Err is a failed read's text, stripped of the wire prefix.
	Err string
	// Dead is a failed read whose verdict was that the id's path ends in
	// nothing (clientsync.OutcomeDead).
	Dead bool
}
