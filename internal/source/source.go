// Package source defines the in-process contract a Gridwell source plugin
// implements. A source projects an external space — a host directory, the
// process table, another Gridwell DB on disk or across a network — into
// Gridwell's own primitives (well / text / url / shell). The store
// reconciles a source grid by calling List; the client never learns a
// source was involved.
//
// The interface is deliberately in-process first (return values, not
// streams): the go-plugin gRPC transport (api/gridwell/v1/data.proto)
// wraps these same methods, but the implementations and their tests prove
// the contract with zero IPC.
//
// The one load-bearing rule: SourceID is an opaque, plugin-owned token
// Gridwell never interprets. A plugin encodes into it whatever path
// context it needs to stay deterministic (fs → an absolute path,
// proc → a PID, a DB proxy → the descent path + grid id).
package source

import (
	"context"
	"errors"
)

// Sentinel errors. Implementations return these (optionally wrapped) so
// the host can map them to the right behavior / status code.
var (
	// ErrUnsupported is returned by a mutation the source does not allow
	// (the matching Caps bit is false).
	ErrUnsupported = errors.New("source: unsupported operation")
	// ErrNotFound is returned when a source_id / key names nothing.
	ErrNotFound = errors.New("source: not found")
	// ErrConflict is returned when a mutation's claimed version does not
	// match the source's current version (optimistic concurrency).
	ErrConflict = errors.New("source: version conflict")
)

// Kind is the Gridwell primitive a projected Node maps to. A source never
// invents kinds; it only ever emits one of these four.
type Kind int

const (
	KindUnspecified Kind = iota
	// KindWell is descendable: the host creates a child source grid keyed
	// by Node.Child and reconciles it with another List call.
	KindWell
	// KindText is markdown content (Node.Body).
	KindText
	// KindURL is a web view: address + frozen preview; rendered live in a
	// local WebContentsView bound to the source DB's session + network.
	KindURL
	// KindShell is a terminal: frozen preview; live PTY streamed from the
	// source (the only live primitive that needs a stream).
	KindShell
)

// Caps are the per-node capabilities, mirroring the host's existing
// per-tile rules. The source is the enforcer; these advertise what it
// will allow so the host/client can gate gestures.
type Caps struct {
	Delete bool // Delete is supported
	Clone  bool // in-source Clone (copy within the source) is supported
	Move   bool // Move (reposition / re-parent) is supported
	Write  bool // Body is editable; UpdateText forwards to Write
	Live   bool // SHELL: a live PTY is attachable now
	Accept bool // WELL: accepts children moved/cloned IN
}

// ContentRef is an opaque, content-addressed handle to a blob. The host
// stores the bytes in its hash-deduped blobs table and skips ReadBlob
// when it already holds Hash. BlobRef is plugin-private (a path, a remote
// blob id); the host never interprets it.
type ContentRef struct {
	BlobRef   string
	MediaType string
	Hash      string // sha256 hex of the content; empty => host must always ReadBlob
	Size      int64
}

// Frame is a well's view framing — preview = descent target = ascent
// return value.
type Frame struct {
	ViewX    int64
	ViewY    int64
	ViewZoom float64
}

// Scroll is a text tile's view window (doc-space px) plus its render mode.
type Scroll struct {
	X, Y, W, H int64
	Mode       string
}

// Node is one projected entry — Gridwell's Tile with identity made opaque
// (Key/Child instead of numeric ids). Only the fields meaningful for Kind
// are set.
type Node struct {
	Key     string // stable dedup id within the parent → tile.source_key
	Kind    Kind
	Label   string // → tile.alt_text
	X, Y    int64
	W, H    int64
	Version int64 // opaque optimistic-concurrency token
	Caps    Caps

	Child string // KindWell: the child node id to descend into
	Frame Frame  // KindWell

	Body     *ContentRef // KindText: markdown
	TextView Scroll      // KindText

	URL     string      // KindURL
	Title   string      // KindURL
	Preview *ContentRef // KindURL / KindShell: frozen jpeg
}

// Presence is the confirmed-absence signal. The host sweeps a tile only
// on PresenceGone, never on PresenceUnknown (a failed read must never
// delete a tile and lose its id/placement).
type Presence int

const (
	PresenceUnknown Presence = iota
	PresencePresent
	PresenceGone
)

// Listing is the result of projecting one source grid.
type Listing struct {
	Nodes []Node
	// Authoritative true: Nodes is the COMPLETE set; the host sweeps any
	// tile whose key is absent (fs semantics). False: the host must Probe
	// each missing key and sweep only on PresenceGone (proc semantics —
	// the list may skip entries it couldn't read this pass).
	Authoritative bool
	Version       int64
}

// ProxyEndpoint is a network proxy the host points a Chromium partition at
// (the data never flows through the plugin RPC surface — only the address
// does).
type ProxyEndpoint struct {
	Scheme  string // e.g. "socks5"
	Address string // e.g. "127.0.0.1:41234"
}

// NetworkContext declares how a source DB's url tiles reach the network:
// Direct (use the host's own network) or via a plugin-provided Proxy (the
// source machine's network, e.g. SOCKS over an SSH tunnel). A nil
// NetworkContext means url tiles are frozen-only.
type NetworkContext struct {
	Direct bool
	Proxy  *ProxyEndpoint
}

// Descriptor is a source's static self-description, returned by Info.
type Descriptor struct {
	Kind          string // the source_kind token, e.g. "fs", "proc", "localdb"
	DisplayName   string
	SchemaVersion int64
}

// Attachment is the result of Attach — a handle to one projected DB.
type Attachment struct {
	RootSourceID string
	Label        string
	Caps         Caps
	Network      *NetworkContext // nil => url tiles frozen-only
	HasSession   bool            // whether GetSession/PutSession are meaningful
}

// MoveRequest / CloneRequest / SetViewRequest carry the structured args
// for the mutations whose signatures don't fit a simple tuple.
type MoveRequest struct {
	SourceID     string
	Key          string
	Version      int64
	DestSourceID string
	X, Y         int64
}

type CloneRequest struct {
	SourceID     string
	Key          string
	Version      int64
	DestSourceID string
	X, Y         int64
}

type SetViewRequest struct {
	SourceID string
	Key      string
	Version  int64
	Frame    *Frame
	Scroll   *Scroll
}

// Source is the contract every plugin implements. All methods are
// host→plugin. Live streaming (shell) and change notification (watch) are
// optional capabilities exposed as separate interfaces (Watcher,
// ShellOpener) so a read-only source need not stub them.
type Source interface {
	// Info returns the static self-description.
	Info() Descriptor

	// Attach turns user config into a root node. Config keys are
	// plugin-specific: fs {path}, proc {pid}, localdb {db_file}.
	Attach(ctx context.Context, config map[string]string) (Attachment, error)
	// Detach releases resources held for a root.
	Detach(ctx context.Context, root string) error

	// List projects a node's children (one source grid).
	List(ctx context.Context, sourceID string) (Listing, error)
	// Probe reports the definitive presence of one key (non-authoritative
	// listings only).
	Probe(ctx context.Context, sourceID, key string) (Presence, error)
	// ReadBlob returns the bytes behind a ContentRef.BlobRef.
	ReadBlob(ctx context.Context, sourceID, blobRef string) ([]byte, error)

	// GetSession / PutSession move the DB's Chromium session blob. A
	// source whose Attachment.HasSession is false may return ErrUnsupported.
	GetSession(ctx context.Context, root string) ([]byte, error)
	PutSession(ctx context.Context, root string, data []byte) error

	// Mutations. Each returns ErrUnsupported when the matching Caps bit is
	// false. Delete's settled return distinguishes "gone now, drop the row"
	// (fs trash) from "best-effort issued, reconcile will sweep" (proc kill).
	Delete(ctx context.Context, sourceID, key string, version int64) (settled bool, err error)
	Move(ctx context.Context, req MoveRequest) (Node, error)
	Clone(ctx context.Context, req CloneRequest) (Node, error)
	Write(ctx context.Context, sourceID, key string, version int64, data []byte) (Node, error)
	SetView(ctx context.Context, req SetViewRequest) (Node, error)
}

// Change is a subtree-invalidation notice: the named source grid is dirty
// and should be re-Listed.
type Change struct {
	SourceID string
}

// Watcher is the optional live-change capability. A source that can push
// (a DB proxy bridging a remote Subscribe stream) implements it; fs/proc
// do not (pull-on-read suffices).
type Watcher interface {
	Watch(ctx context.Context, root string) (<-chan Change, error)
}
