// Package rpc is the Go side of the Gridwell RPC service: the id codec, the
// kind and glyph vocabularies, the tile predicates, the content stream's
// bounds, the beacon bodies and the Client. data.proto is the one description
// of a record and of the wire.
package rpc

import (
	"cmp"
	"strconv"
	"strings"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Tile kinds. A tile is exactly one of these.
const (
	KindDoor      = "door"
	KindText      = "text"
	KindURL       = "url"
	KindShell     = "shell"
	KindWorkspace = "workspace"
)

// KindCaps is what a tile kind can do; a site asks it, never which kind it is.
type KindCaps struct {
	Descends  Descends // what a descent through the tile enters
	Live      Descent  // the surface a descent opens, and a freeze keeps a screenshot of
	Content   Content  // what the content is and where it lives
	Tmux      bool     // the tile names a tmux (ShellTmux)
	TypedName bool     // the name is typed, not the content's first line
}

// kinds is the kind table; an unknown kind reads the zero row, which can do nothing.
var kinds = map[string]KindCaps{
	KindDoor:      {Descends: DescendGrid, TypedName: true},
	KindText:      {Descends: DescendContent, Content: ContentBody},
	KindURL:       {Descends: DescendContent, Live: DescentURL, Content: ContentAddress, TypedName: true},
	KindShell:     {Descends: DescendContent, Live: DescentShell, Tmux: true, TypedName: true},
	KindWorkspace: {Descends: DescendLevel, Content: ContentLayout, TypedName: true},
}

// KindOf is kind's row of the kind table.
func KindOf(kind string) KindCaps { return kinds[kind] }

// Freezes: the kind keeps a screenshot as its preview, which a freeze holds.
func (k KindCaps) Freezes() bool { return k.Live != DescentNone }

// Descends is what a descent through a tile enters.
type Descends int

const (
	DescendNone    Descends = iota
	DescendGrid             // pushes the grid the tile opens
	DescendContent          // goes live in the tile itself (pane.TextFocus)
	DescendLevel            // swaps the whole pane tree and pushes a level
)

// Content is what a tile's content is and where it lives.
type Content int

const (
	ContentNone    Content = iota
	ContentBody            // a document body in the tile's blob
	ContentLayout          // a pane layout in the tile's blob (api/panelayout)
	ContentAddress         // the address the tile holds, url_string
)

// AddressMediaType is the media type a read of an address answers under.
const AddressMediaType = "text/plain; charset=utf-8"

// InBlob: the content is bytes in a blob of the tile's own.
func (c Content) InBlob() bool { return c == ContentBody || c == ContentLayout }

// ContentChunkBytes is the size of every content chunk but the last, for
// every producer of a content stream.
const ContentChunkBytes = 256 * 1024

// MaxContentBytes is the largest content body an API carries, either way;
// store.MaxBlobBytes matches it.
const MaxContentBytes = 16 * 1024 * 1024

// IsDoorKind: a door to another namespace is still a door, said by its child_grid_id and not
// by its kind.
func IsDoorKind(kind string) bool { return KindOf(kind).Descends == DescendGrid }

// "<uuid>/<local>" is the qualified id convention; QualifyID, SplitID,
// UUIDOf and NamespaceOf are its only encode/decode points.
func QualifyID(uuid, local string) string { return uuid + "/" + local }

// QualifyOptionalID is QualifyID for an id that may be absent, which stays "".
func QualifyOptionalID(uuid, local string) string {
	if local == "" {
		return ""
	}
	return QualifyID(uuid, local)
}

// QualifyIDs is QualifyID over a list, into a new one.
func QualifyIDs(uuid string, locals []string) []string {
	if locals == nil {
		return nil
	}
	out := make([]string, len(locals))
	for i, l := range locals {
		out[i] = QualifyID(uuid, l)
	}
	return out
}

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

// IsDoorToOtherNamespace compares whole sources (SourceOf), since behind a
// connection every id shares its first segment and home's ids share the files'
// node; a SourceDoorTile, whose GridID is empty, is one.
func IsDoorToOtherNamespace(t *pb.Tile) bool {
	return IsDoorKind(t.Kind) && t.ChildGridId != "" &&
		SourceOf(t.ChildGridId) != SourceOf(t.GridId)
}

// NamespaceOf is everything before a qualified id's last segment. Equal
// namespaces is the test for "same store", which no single-segment comparison
// answers once ids chain through connections.
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

// SourceDoorTile is the synthetic link door a roster row is drawn as outside
// a real grid: the drag ghost and the menu swatch.
func SourceDoorTile(pl *pb.SourceInfo) *pb.Tile {
	return &pb.Tile{
		Kind:        KindDoor,
		W:           1,
		H:           1,
		AltText:     pl.Label,
		ChildGridId: pl.RootGridId,
		Link:        true,
		ViewCx:      pl.RootViewCx,
		ViewCy:      pl.RootViewCy,
		ViewZoom:    pl.RootViewZoom,
	}
}

// ConnectionRow is the one shape a connection takes in the + menu: a roster
// row marked connection, its landing as the root and the last dial failure
// as InfoError. A pending connection is rootless, so health reads it as
// waiting, not broken.
func ConnectionRow(uuid, label, rootGridID, statusDetail string, view View) *pb.SourceInfo {
	cx, cy, zoom := view.Wire()
	return &pb.SourceInfo{
		Uuid: uuid, Connection: true, Label: label, Glyph: GlyphGlobe,
		RootGridId: rootGridID, InfoError: statusDetail,
		RootViewCx: cx, RootViewCy: cy, RootViewZoom: zoom,
	}
}

// IsConnectionRow is the one fact that tells a connection row from home's
// and the files'; never the shape of the uuid.
func IsConnectionRow(pl *pb.SourceInfo) bool { return pl.GetConnection() }

// HomeGrid is the qualified grid id "/" means: the handshake's home_grid_id,
// a field and never a row's position. "" is a node that learned no home.
func HomeGrid(l *pb.HandshakeResponse) string {
	return l.GetHomeGridId()
}

// HomeRow is the roster row rooted at HomeGrid, the one owner of the home's
// framing; nil when the node learned no home.
func HomeRow(l *pb.HandshakeResponse) *pb.SourceInfo {
	home := HomeGrid(l)
	if home == "" {
		return nil
	}
	for _, p := range l.GetSources() {
		if p.GetRootGridId() == home {
			return p
		}
	}
	return nil
}

// IsContentDescentKind: a descent goes live in the tile, DescendContent.
func IsContentDescentKind(kind string) bool { return KindOf(kind).Descends == DescendContent }

// IsWorkspaceKind: a descent swaps the pane tree, DescendLevel.
func IsWorkspaceKind(kind string) bool { return KindOf(kind).Descends == DescendLevel }

// IsBodyKind: these kinds hold a content blob of their own.
func IsBodyKind(kind string) bool { return KindOf(kind).Content.InBlob() }

// The glyph vocabulary: named by a source, rendered by the client, an
// unknown name falling back to the globe. A row declaring nothing takes
// the grid glyph instead (client/door.RowGlyph).
const (
	GlyphFolder = "folder"
	GlyphDoor   = "door"
	GlyphTrash  = "trash"
	GlyphGlobe  = "globe"
)

// Text-tile display modes.
const (
	TextModeRendered = "rendered"
	TextModeText     = "text"
)

// WebContent is a url tile: at the address it carries, or at the /content/
// API, the node serving the page.
func WebContent(t *pb.Tile) bool { return KindOf(t.Kind).Content == ContentAddress }

// TextDocument is a tile whose content is its own document body. Every text
// tile has one: a served page is a url tile (files.fileFacts).
func TextDocument(t *pb.Tile) bool { return KindOf(t.Kind).Content == ContentBody }

// HTTPAddress is an address a url tile may hold: http or https, nothing else.
func HTTPAddress(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// PageContent is a url tile whose source serves the page, at PageURL.
func PageContent(t *pb.Tile) bool { return WebContent(t) && t.ServesPage }

// Descent is the content family a pane descended into a tile is showing: a
// web surface, a terminal, or neither.
type Descent int

const (
	DescentNone Descent = iota
	DescentURL
	DescentShell
)

// DescentOf classifies the tile a pane is descended into; nil is DescentNone.
func DescentOf(t *pb.Tile) Descent { return KindOf(t.GetKind()).Live }

// Ephemeral reports that grid is the scratch grid, so a tile on it is an ephemeral visit.
func Ephemeral(scratch, grid string) bool { return scratch != "" && grid == scratch }

// LeafLink is one content tile shown in a second place, owning no bytes of its
// own. ContentID answers whose content it is.
func LeafLink(t *pb.Tile) bool {
	return t.LinkTargetId != ""
}

// QualifyLinkTarget is a leaf link's target as the node holding the link
// spells it. A target with no namespace segment is the holder's own, the way
// the files link a symlink to its target's path; any other target is
// already the holder's spelling.
func QualifyLinkTarget(holder, target string) string {
	if _, _, qualified := SplitID(target); qualified || target == "" {
		return target
	}
	return QualifyID(holder, target)
}

// ContentPath is where the node's content API is mounted.
const ContentPath = "/content/"

// PageURL mirrors the server's parseContentPath: the token, then the
// qualified tile id, and nothing after it.
func PageURL(origin, contentToken, tileID string) string {
	return origin + ContentPath + contentToken + "/" + tileID
}

// PageRange is the bytes [from, from+n) of a page of size that a
// ServeContentRequest's offset and length ask: offset below 0 counts back
// from the end, and length 0 runs to it. ok is false for a range that starts
// at or past the end of a page that has bytes, or of an empty one asked for
// its last bytes.
func PageRange(offset, length, size int64) (from, n int64, ok bool) {
	from = offset
	if from < 0 {
		from = max(0, size+from)
	}
	if from >= size && (size > 0 || offset < 0) {
		return 0, 0, false
	}
	n = size - from
	if length > 0 {
		n = min(n, length)
	}
	return from, n, true
}

// The text_presentation vocabulary: whether a file's text body is a
// document. There is no document-only value, because the user can always
// reach the source bytes.
const (
	TextPresentationPlain = "plain"
	TextPresentationBoth  = "both"
)

// ContentBasis names a body's bytes as their owner does: the row version for a
// versioned body, the source's stamp (Tile.content_stamp) for a claimless one.
// A read answers it with the bytes, and a write claims it.
type ContentBasis struct {
	Version int64
	Stamp   string
}

// BasisOf is the basis row t names for the bytes behind it.
func BasisOf(t *pb.Tile) ContentBasis {
	return ContentBasis{Version: t.GetVersion(), Stamp: t.GetContentStamp()}
}

// PlacementOf is where t sits, as a claim names it.
func PlacementOf(t *pb.Tile) *pb.Placement {
	return &pb.Placement{GridId: t.GetGridId(), X: t.GetX(), Y: t.GetY(), W: t.GetW(), H: t.GetH()}
}

// ContentID is the tile id that owns a tile's content: a leaf link's target, or
// the tile's own id.
func ContentID(t *pb.Tile) string {
	if LeafLink(t) {
		return t.LinkTargetId
	}
	return t.Id
}

// LinkOf is the tile a link to src creates at (x, y), src's size: a door
// to another namespace onto a door's child grid with its framing, or a leaf link to the
// content src names (ContentID), so a link to a link names the content and
// links never chain through middleman tiles.
func LinkOf(src *pb.Tile, x, y int64) *pb.Tile {
	if IsDoorKind(src.Kind) {
		return &pb.Tile{Kind: KindDoor, X: x, Y: y, W: src.W, H: src.H,
			ChildGridId: src.ChildGridId, AltText: src.AltText,
			ViewCx: src.ViewCx, ViewCy: src.ViewCy, ViewZoom: src.ViewZoom}
	}
	return &pb.Tile{Kind: src.Kind, X: x, Y: y, W: src.W, H: src.H,
		LinkTargetId: ContentID(src), AltText: src.AltText}
}

// CopiesAsLink reports that a copy of t, a file that is not markdown, a
// page file among them, is a link, since its bytes stay on disk (CLAUDE.md,
// 2026-11-08). CopyOf owns every other case.
func CopiesAsLink(t *pb.Tile) bool {
	p, file := FilePath(t.GetId())
	return file && !IsDoorKind(t.GetKind()) && !Markdown(p)
}

// CopyAs is what a copy of a tile becomes; CopyOf decides it.
type CopyAs int

const (
	CopyRefused  CopyAs = iota // no copy; the target is the reason
	CopyLinkGrid               // a door linking to the target grid
	CopyLinkTile               // a leaf linking to the target tile
	CopyDeep                   // a new door and a copy of the target grid beneath it
	CopyLeaf                   // a new tile of the source's bytes and CopyFacts
	CopyShell                  // a shell naming the target tmux, the source's
)

// CopyOf is the one owner of what a copy of t becomes and the id it names,
// read by the router's walk and the store's copy alike. t.Link is the wire's
// bit for a door onto another namespace's grid; accepts, whether the grid a
// door opens takes new tiles, is asked only of a door that is not a link.
func CopyOf(t *pb.Tile, sameSource bool, accepts func() bool) (CopyAs, string) {
	k := KindOf(t.GetKind())
	switch {
	case LeafLink(t):
		return CopyLinkTile, t.GetLinkTargetId()
	case IsDoorKind(t.GetKind()) && (t.GetLink() || !accepts()):
		return CopyLinkGrid, cmp.Or(t.GetChildGridId(), t.GetId())
	case IsDoorKind(t.GetKind()):
		return CopyDeep, t.GetChildGridId()
	case k.Tmux && sameSource:
		return CopyShell, ShellTmux(t)
	case k.Tmux || CopiesAsLink(t):
		// A tmux lives on the node that started it.
		return CopyLinkTile, t.GetId()
	case k.Content != ContentNone:
		return CopyLeaf, ""
	}
	return CopyRefused, "unsupported tile kind " + strconv.Quote(t.GetKind())
}

// Markdown is a file name a copy carries the bytes of, into the files or out
// of them: one ending in .md, in any case.
func Markdown(name string) bool { return strings.HasSuffix(strings.ToLower(name), ".md") }

// IntoDirectory is why a directory refuses t, copied or created there, and ""
// when it takes it: a text tile, which it makes a new markdown file of its
// body. The drop verdict and the node both read it.
func IntoDirectory(t *pb.Tile) string {
	switch {
	case t.GetLink() || LeafLink(t) || CopiesAsLink(t):
		return "a directory takes no link, and a file that is not markdown copies as one"
	case t.GetKind() != KindText:
		return "a directory takes a text tile alone, made a markdown file, and refuses a " + t.GetKind()
	}
	return ""
}

// ShellTmux names the tmux a shell tile attaches: the tmux its
// row names, which a copy shares with its source, or else its content tile's.
func ShellTmux(t *pb.Tile) string {
	if t.ShellTmux != "" {
		return t.ShellTmux
	}
	return ContentID(t)
}
