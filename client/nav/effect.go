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
	// into a tile: PaneID, DestPaneID, TileID, Foot, Zoom. (Promote's.)
	EffRelocatePane
	// EffForgetPane drops every session resource keyed to a pane: PaneID.
	EffForgetPane
	// EffInstallLevel swaps the whole pane tree for a pane-tile level:
	// Level (everything but the outer tree, which is the live one), Tree
	// (nil with Capture set: the tree to install is the window layout as it
	// stands at the swap), Baseline, KeepOuter, IDPrefix.
	EffInstallLevel
	// EffPopLevel leaves one pane-tile level, restoring the tree it parked —
	// or, when it parked none, a fresh pane at GridID: OriginPane, TileID,
	// GridID.
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
	EffFlushDirtyText
	// EffFlushLayout persists the pane-tile layout blob now. No payload.
	EffFlushLayout
	// EffFlushDroppedSubtree flushes the writebacks a closing subtree owes.
	// No payload.
	EffFlushDroppedSubtree

	// EffCancelTransition lands whatever a pane is animating, on its
	// destination: PaneID ("" = every pane).
	EffCancelTransition
	// EffStartTransition animates a pane: PaneID, Segments, TraceTileID,
	// Land (the continuation the executor resumes from OnComplete; a
	// cancelled transition still lands, so the resume happens either way).
	// Expand, with Tile, asks for the pane-tile capture animation over the
	// same clock: the tile's face growing into the level outline while the
	// content underneath never moves, which is what a first descent looks
	// like instead of a zoom.
	EffStartTransition

	// EffCloseStream tears a pane's live surfaces down: PaneID, Streams,
	// Freeze, FreezeOnto.
	EffCloseStream
	// EffOpenStream opens one: PaneID, TileID, Stream.
	EffOpenStream
	// EffPlaceURLView re-parents a live url view onto a pane: PaneID,
	// TileID. (Promote's.)
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
	// EffWriteURLNow writes the history entry now. No payload.
	EffWriteURLNow
	// EffPlaceCursor puts the text cursor at a document position: Col, Row.
	EffPlaceCursor

	// EffDeleteEphemeral deletes an ascended-from ephemeral visit: GridID,
	// TileID.
	EffDeleteEphemeral
	// EffReport surfaces a notice: Severity, Source, Message.
	EffReport
	// EffEnterLevel descends the window into a pane tile: PaneID, TileID,
	// Tile (the descent-time row, by value, for the same reason the Descend
	// gesture carries one). Like EffReEngage it re-enters the machine
	// through its own gesture, which is planned against a world gathered
	// after the effects above it.
	EffEnterLevel
	// EffLeaveLevels leaves pane-tile levels: Count. It re-enters the machine
	// the same way.
	EffLeaveLevels
	// EffReEngage re-engages a restored content frame: PaneID, TileID. It
	// re-enters the machine through GestureReEngage, which reads the row and
	// engages under a DescendedIn guard.
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
	Capture    bool
	IDPrefix   string
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
	// RequestGetTile reads one tile row by id: ID. The answer is cached by
	// the executor before the resume, so the row the machine acts on and the
	// row the renderer draws are the same one.
	RequestGetTile RequestKind = iota
	// RequestGetGrid reads one grid: ID. OK reports whether it loaded; the
	// walk asks for a grid at most once, however it answered.
	RequestGetGrid
	// RequestReadContent reads a tile's body into the cache: ID.
	RequestReadContent
	// RequestReadLayout reads a pane tile's layout blob and hands the BYTES
	// back (Data): ID. It is separate from RequestReadContent because a
	// layout is not a document — it never seeds the text overlay, and it is
	// decoded here rather than cached as a body.
	RequestReadLayout
	// RequestSearch locates a tile: Query, Scope, Limit.
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
	// OK is false when the read failed, or answered nothing usable — a
	// search with no hit is the same "no" as a search that could not run,
	// and both leave the pane where it is. A step that cannot act on a
	// failure simply plans nothing.
	OK bool
	// Alive answers RequestProbeShell.
	Alive bool
	// Tile answers RequestGetTile: the row BY VALUE, because the step acts on
	// the row that was read rather than on whatever the cache holds later.
	Tile *rpc.Tile
	// Wells answers RequestSearch: the hit's containing-well chain from its
	// plugin root, outermost first. Empty means the tile sits at a root.
	Wells []rpc.Tile
	// Data answers RequestReadLayout: the blob's bytes, which the machine
	// decodes itself (client/pane owns the codec, and it is pure).
	Data []byte
	// Err is a failed read's text, already stripped of the wire prefix. A
	// step that surfaces its failure — a level descent aborts something the
	// user is watching — plans the notice with it; one that stays quiet
	// ignores it.
	Err string
}
