// Package rpc is the Go side of the Gridwell RPC service: the id codec, the
// kind and glyph vocabularies, the tile predicates, the content stream's
// bounds, the beacon bodies and the Client. data.proto is the one description
// of a record and of the wire.
package rpc

import (
	"math"
	"strings"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Tile kinds. A tile is exactly one of these. A shell's contents reflect state
// the host owns, not Gridwell, and the red-outline grammar follows.
const (
	KindWell  = "well"
	KindText  = "text"
	KindURL   = "url"
	KindShell = "shell"
	// KindPane is a durable layout: a tile whose content blob is a
	// serialized split-pane layout (api/panelayout). The string is frozen
	// into the store's CHECK.
	KindPane = "pane"
)

// ContentChunkBytes is the size of every content chunk but the last: small
// enough to stream a large body without one giant message, large enough that a
// typical text tile is one chunk. Every producer of a content stream uses it,
// so no reader can tell one producer from another by the framing.
const ContentChunkBytes = 256 * 1024

// MaxContentBytes is the largest content body the door carries, whichever way
// it flows: what a write is refused above, what a cache declines to remember,
// what the store's blob column holds (store.MaxBlobBytes).
const MaxContentBytes = 16 * 1024 * 1024

// IsWellKind: an exit well is still a well, said by its child_grid_id and not
// by its kind.
func IsWellKind(kind string) bool {
	return kind == KindWell
}

// "<uuid>/<local>" is the cross-plugin id convention; QualifyID, SplitID,
// UUIDOf and NamespaceOf are its only encode/decode points.
func QualifyID(uuid, local string) string { return uuid + "/" + local }

// QualifyNS prepends one hop segment to a namespace chain. Distinct from
// QualifyID because a namespace may legitimately be empty.
func QualifyNS(hop, ns string) string {
	if ns == "" {
		return hop
	}
	return hop + "/" + ns
}

// SplitID is the one-hop routing peel; rest may itself be a chain, and ok is
// false for a bare id.
func SplitID(id string) (uuid, rest string, ok bool) {
	if i := strings.IndexByte(id, '/'); i > 0 {
		return id[:i], id[i+1:], true
	}
	return "", "", false
}

// UUIDOf is the first segment, "" when the id is bare.
func UUIDOf(id string) string {
	uuid, _, _ := SplitID(id)
	return uuid
}

// IsExitWell reads only the two qualified ids, so a PluginWellTile, whose
// GridID is empty, is one and previews the plugin's grid.
func IsExitWell(t *pb.Tile) bool {
	return IsWellKind(t.Kind) && t.ChildGridId != "" &&
		UUIDOf(t.ChildGridId) != UUIDOf(t.GridId)
}

// NamespaceOf is everything before a qualified id's last segment. Equal
// namespaces is the test for "same store", which no single-segment comparison
// answers once ids chain through mounts.
func NamespaceOf(id string) string {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		return id[:i]
	}
	return ""
}

// LocalOf is a qualified id's last segment.
func LocalOf(id string) string {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		return id[i+1:]
	}
	return id
}

// PluginWellTile is the synthetic exit-well tile a plugin is rendered as
// outside a real grid: the drag ghost and the menu swatch.
func PluginWellTile(pl *pb.PluginInfo) *pb.Tile {
	return &pb.Tile{
		Kind:        KindWell,
		W:           1,
		H:           1,
		AltText:     pl.Label,
		ChildGridId: pl.RootGridId,
		// A menu swatch is a link by nature, so it renders dashed like a
		// mounted plugin well.
		Reference: true,
		// The plugin's persisted root framing carries across verbatim, so
		// the synthetic tile lands at the left-off view.
		ViewCx:   pl.RootViewCx,
		ViewCy:   pl.RootViewCy,
		ViewZoom: pl.RootViewZoom,
	}
}

// PluginKindConnection is the one fact that tells a connection row from a
// plugin row; never the shape of the uuid.
const PluginKindConnection = "connection"

// ConnectionRow presents a connection as a menu row, the one shape every menu
// flow already handles. A pending connection is rootless with its failure in
// StatusDetail, so health reads it as waiting, not broken.
func ConnectionRow(c *pb.ConnectionInfo) *pb.PluginInfo {
	return &pb.PluginInfo{
		Uuid: c.Uuid, Kind: PluginKindConnection, Label: c.Label, Glyph: GlyphGlobe,
		RootGridId: c.RootGridId, InfoError: c.StatusDetail,
		RootViewCx: c.RootViewCx, RootViewCy: c.RootViewCy, RootViewZoom: c.RootViewZoom,
	}
}

// MenuRows is the + menu's top row: the node's plugins, home first, then its
// connections.
func MenuRows(l *pb.HandshakeResponse) []*pb.PluginInfo {
	out := make([]*pb.PluginInfo, 0, len(l.Plugins)+len(l.Connections))
	out = append(out, l.Plugins...)
	for _, c := range l.Connections {
		out = append(out, ConnectionRow(c))
	}
	return out
}

// HomeGrid is the qualified grid id "/" means, falling back to the first
// rooted row for a node that does not send home_grid_id.
func HomeGrid(l *pb.HandshakeResponse) string {
	if l.HomeGridId != "" {
		return l.HomeGridId
	}
	for _, pl := range l.Plugins {
		if pl.RootGridId != "" {
			return pl.RootGridId
		}
	}
	return ""
}

// IsContentDescentKind: descending one of these sets pane.TextFocus rather
// than pushing a grid. Click-to-descend and the URL-restore walk share it, or
// a descent encoded into the URL would be dropped on reload.
func IsContentDescentKind(kind string) bool {
	return kind == KindText || kind == KindURL || kind == KindShell
}

// IsWorkspaceKind: a pane tile's descent swaps the whole pane tree and pushes
// a level. With IsWellKind and IsContentDescentKind it partitions the
// descendable kinds, pinned so a new kind cannot fall through a dispatch.
func IsWorkspaceKind(kind string) bool {
	return kind == KindPane
}

// IsBodyKind: these kinds hold a content blob of their own. The deep copy
// carries it, the prefetch walk warms it and the store refcounts it, so no
// walker decides for itself what has bytes.
func IsBodyKind(kind string) bool {
	return kind == KindText || kind == KindPane
}

// The plugin glyph vocabulary: declared by the plugin, rendered by the
// client, an unknown name falling back to the globe so a third-party plugin
// degrades without either side learning names. A row declaring nothing takes
// the grid face instead (client/door.RowGlyph).
const (
	GlyphFolder  = "folder"
	GlyphProcess = "process"
	GlyphWell    = "well"
	GlyphTrash   = "trash"
	GlyphGlobe   = "globe"
)

// Text-tile display modes.
const (
	TextModeRendered = "rendered"
	TextModeText     = "text"
)

// WebContent is a url tile at its own address or a serves_page tile at the
// /content/ door. Every url-tile semantic keys off it, so the two cannot
// diverge.
func WebContent(t *pb.Tile) bool {
	return t.Kind == KindURL || t.ServesPage
}

// TextDocument is a tile whose content is its own document body; a serves_page
// row is a file presented as a page and has none.
func TextDocument(t *pb.Tile) bool {
	return t.Kind == KindText && !t.ServesPage
}

// PageContent is a tile presented at the /content/ door. It holds no persisted
// url state, zoom or freeze intent, so its address is derived at use time
// (PageURL); a url tile is never one however it is flagged.
func PageContent(t *pb.Tile) bool {
	return t.ServesPage && t.Kind != KindURL
}

// LeafLink is one content tile shown in a second place, owning no bytes of its
// own. ContentID answers whose content it is.
func LeafLink(t *pb.Tile) bool {
	return t.LinkTargetId != ""
}

// PageURL mirrors the server's parseContentPath. The trailing slash is
// load-bearing: relative subresource URLs resolve against the directory.
func PageURL(origin, contentToken, tileID string) string {
	return origin + "/content/" + contentToken + "/" + tileID + "/"
}

// The text_presentation vocabulary.
const (
	TextPresentationPlain    = "plain"
	TextPresentationRendered = "rendered"
	TextPresentationBoth     = "both"
)

// ContentID is the tile id that owns a tile's content: a leaf link's target, or
// the tile's own id. Every client content operation keys by it, so a link and
// its target share one content fact and no write lands on a link row.
func ContentID(t *pb.Tile) string {
	if t.LinkTargetId != "" {
		return t.LinkTargetId
	}
	return t.Id
}

// Framing is how a grid looked when it was last left through a doorway: a
// float center in the grid's own coordinates plus a pane-size-independent
// zoom, so a window resize never moves a saved view. Zoom == 0 means never
// visited, and Cx and Cy mean nothing then.
type Framing struct {
	Cx   float64
	Cy   float64
	Zoom float64
}

// framingEpsilon is how close two framings count as the same picture. Below
// it a write is float jitter in a re-derived viewport, not a place the user
// chose.
const framingEpsilon = 0.001

// SameAs is the same-framing test, within framingEpsilon. Every persister
// consults it, so a settle tick never churns the store.
func (f Framing) SameAs(g Framing) bool {
	return math.Abs(f.Cx-g.Cx) < framingEpsilon &&
		math.Abs(f.Cy-g.Cy) < framingEpsilon &&
		math.Abs(f.Zoom-g.Zoom) < framingEpsilon
}
