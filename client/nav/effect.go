package nav

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/transition"
)

// The effect vocabulary: everything navigation asks the shim to do, derived
// from what nav.go, input.go, urlsync.go and workspace.go already did. It is
// frozen — phases B and C emit more of it, but they add no verbs.
//
// It is one tagged struct rather than an interface per effect, the same shape
// pane.TreeNode uses for its sum: a plan is then comparable field by field in
// a table test, and no effect can be constructed without a Kind. Each kind's
// doc comment names the fields it reads; everything else is zero.
//
// There is no Redraw. The executor draws once after a plan, always.
type EffectKind int

const (
	// EffInstallPlace installs a pane's place, and optionally the viewport
	// it lands at: PaneID, Stack, Viewport (nil keeps the pane's own).
	EffInstallPlace EffectKind = iota
	// EffClearSelection drops the pane's selection: PaneID.
	EffClearSelection
	// EffRelocatePane moves a pane to where another stands and descends it
	// into a tile: PaneID, DestPaneID, TileID, Foot, Zoom. (Phase C.)
	EffRelocatePane
	// EffForgetPane drops every session resource keyed to a pane: PaneID.
	// (Phase C.)
	EffForgetPane
	// EffInstallLevel swaps the whole pane tree for a pane-tile level:
	// Level, Tree, Baseline, KeepOuter. (Phase C.)
	EffInstallLevel
	// EffPopLevel leaves one pane-tile level: Animate, OriginPane.
	// (Phase C.)
	EffPopLevel

	// EffFlushFraming persists every pane's settled grid framing now. The
	// pane.FramingWriters rule stays in the executor. No payload.
	EffFlushFraming
	// EffPersistFraming writes one pane's framing onto the row that owns it:
	// PaneID, Owner, Door (true for the doorway arm, false for the root
	// arm).
	EffPersistFraming
	// EffSaveText posts a content pane's editor buffer and framed window:
	// PaneID, TileID.
	EffSaveText
	// EffFlushDirtyText flushes every unsaved edit now. No payload.
	// (Phase C.)
	EffFlushDirtyText
	// EffFlushLayout persists the pane-tile layout blob now. No payload.
	// (Phase C.)
	EffFlushLayout
	// EffFlushDroppedSubtree flushes the writebacks a closing subtree owes.
	// No payload. (Phase C.)
	EffFlushDroppedSubtree

	// EffCancelTransition lands whatever a pane is animating, on its
	// destination: PaneID ("" = every pane).
	EffCancelTransition
	// EffStartTransition animates a pane: PaneID, Segments, TraceTileID,
	// Land (the continuation the executor resumes from OnComplete; a
	// cancelled transition still lands, so the resume happens either way).
	EffStartTransition

	// EffCloseStream tears a pane's live surfaces down: PaneID, Streams,
	// Freeze, FreezeOnto.
	EffCloseStream
	// EffOpenStream opens one: PaneID, TileID, Stream.
	EffOpenStream
	// EffPlaceURLView re-parents a live url view onto a pane: PaneID,
	// TileID. (Phase C.)
	EffPlaceURLView
	// EffRefreshOverlay re-syncs the text and rendered overlays. No payload.
	EffRefreshOverlay
	// EffScaleContent re-derives a pane's content render scale from its rect
	// and the tile's intrinsic zoom (issue #82): PaneID. Emitted wherever a
	// pane lands on or installs a content frame.
	EffScaleContent

	// EffFetchGrid warms a grid: GridID, or PaneID for "the grid this pane's
	// place names", which only the executor can resolve (the walk reads the
	// cache and kicks its own fetches).
	EffFetchGrid
	// EffFetchTileContent warms a tile's body: TileID.
	EffFetchTileContent
	// EffDropTileContent drops a cached body before refetching it:
	// ContentID.
	EffDropTileContent
	// EffAwait starts an async read the machine will be resumed with: Token,
	// Request.
	EffAwait

	// EffOpenMenu reopens the + menu on a pane, and clears the frame flag
	// that asked for it: PaneID.
	EffOpenMenu
	// EffCloseMenu closes the + menu. No payload.
	EffCloseMenu
	// EffScheduleURLUpdate arms the debounced history writer. No payload.
	EffScheduleURLUpdate
	// EffWriteURLNow writes the history entry now. No payload. (Phase B.)
	EffWriteURLNow
	// EffPlaceCursor puts the text cursor at a document position: Col, Row.
	// (Phase B.)
	EffPlaceCursor

	// EffDeleteEphemeral deletes an ascended-from ephemeral visit: GridID,
	// TileID.
	EffDeleteEphemeral
	// EffReport surfaces a notice: Severity, Source, Message.
	EffReport
	// EffEnterLevel descends the window into a pane tile: PaneID, TileID,
	// Tile (the descent-time row, by value, for the same reason the Descend
	// gesture carries one).
	EffEnterLevel
	// EffLeaveLevels leaves pane-tile levels: Count.
	EffLeaveLevels
	// EffReEngage re-engages a restored content frame: PaneID, TileID.
	// Phase A runs the existing restore path unchanged; phase B replaces its
	// body with Await{GetTile} under a DescendedIn guard.
	EffReEngage
)

// StreamKind names one live surface class.
type StreamKind int

const (
	// StreamURL is the native url view.
	StreamURL StreamKind = iota
	// StreamShell is the PTY attachment.
	StreamShell
	// StreamBoth is both at once — the teardown for a content frame whose
	// row is gone, where nothing says which kind it was.
	StreamBoth
)

// FreezeTarget names the row a closing url view's capture is persisted onto
// when it is not the row the view was opened for: a promoted visit freezes
// onto the tile it became.
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
	// Tile is a row by value, for the effects that must act on the row the
	// gesture read rather than on whatever the cache holds later: an
	// ephemeral scratch tile is in no cached grid.
	Tile rpc.Tile

	// Place and tree.
	Stack      *pane.Stack
	Viewport   *Viewport
	Foot       pane.Footprint
	Zoom       float64
	Level      *pane.Level
	Tree       *pane.Tree
	Baseline   []byte
	KeepOuter  bool
	Animate    bool
	OriginPane string
	Count      int

	// Writeback.
	Owner pane.FramingOwner
	Door  bool

	// Transition.
	Segments    []transition.Segment
	TraceTileID string
	Land        Token

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
	// RequestGetTile reads one tile row by id: ID. (Phase B.)
	RequestGetTile RequestKind = iota
	// RequestGetGrid reads one grid: ID. (Phase B.)
	RequestGetGrid
	// RequestReadContent reads a tile's body: ID. (Phase B.)
	RequestReadContent
	// RequestSearch locates a tile: Query, Scope, Limit. (Phase B.)
	RequestSearch
	// RequestProbeShell asks whether a shell session is still alive: ID is
	// the content id the shell facts key by.
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
	// OK is false when the read failed; a step that cannot act on a failure
	// simply plans nothing.
	OK bool
	// Alive answers RequestProbeShell.
	Alive bool
}
