// Package localdb implements a Gridwell source that projects another
// Gridwell DB on disk. It is the simplest "a source is a Gridwell DB"
// plugin — and the one that exercises the session RPCs — without the SSH
// transport. The remote SSH plugin is this same projection over a bridged
// connection.
//
// SourceID encodes a cursor {Root, Path, Grid}: Root identifies the
// attached DB, Path is the chain of underlying well-tile ids walked to
// reach Grid (so forwarded mutations carry the underlying DB's COW path),
// and Grid is the underlying grid being listed. The token is opaque to
// Gridwell.
package localdb

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	_ "modernc.org/sqlite"

	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/source"
	"github.com/josephburnett/gridwell/internal/store"
)

// cursor is the decoded form of a SourceID.
type cursor struct {
	Root string  `json:"r"` // attached DB key (its file path)
	Path []int64 `json:"p"` // underlying well-tile ids from the DB root
	Grid int64   `json:"g"` // underlying grid id being listed
}

func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, fmt.Errorf("%w: bad cursor", source.ErrNotFound)
	}
	var c cursor
	if err := json.Unmarshal(b, &c); err != nil {
		return cursor{}, fmt.Errorf("%w: bad cursor", source.ErrNotFound)
	}
	return c, nil
}

// attachment is one opened DB: the store for projection + a separate
// connection for the per-DB session blob.
type attachment struct {
	file  string
	st    *store.Store
	sess  *sql.DB
	label string
}

// Source projects Gridwell DB files. One Source instance can hold several
// attached DBs, keyed by file path.
type Source struct {
	mu  sync.Mutex
	dbs map[string]*attachment
}

func New() *Source { return &Source{dbs: map[string]*attachment{}} }

var _ source.Source = (*Source)(nil)

func (s *Source) Info() source.Descriptor {
	return source.Descriptor{Kind: "localdb", DisplayName: "database", SchemaVersion: 1}
}

// Attach opens the DB file, prepares its session table, and returns a root
// cursor. A local DB uses the host's own network for its url tiles and
// carries a session.
func (s *Source) Attach(ctx context.Context, config map[string]string) (source.Attachment, error) {
	file := config["db_file"]
	if file == "" {
		return source.Attachment{}, fmt.Errorf("%w: localdb requires db_file", source.ErrNotFound)
	}
	file, err := filepath.Abs(file)
	if err != nil {
		return source.Attachment{}, err
	}

	st, err := store.Open(file)
	if err != nil {
		return source.Attachment{}, fmt.Errorf("open db: %w", err)
	}
	rootGrid, err := st.RootGridID(ctx)
	if err != nil {
		st.Close()
		return source.Attachment{}, fmt.Errorf("root grid: %w", err)
	}
	sess, err := openSession(file)
	if err != nil {
		st.Close()
		return source.Attachment{}, err
	}

	label := config["label"]
	if label == "" {
		label = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	}

	s.mu.Lock()
	s.dbs[file] = &attachment{file: file, st: st, sess: sess, label: label}
	s.mu.Unlock()

	return source.Attachment{
		RootSourceID: encodeCursor(cursor{Root: file, Grid: rootGrid}),
		Label:        label,
		Caps:         source.Caps{},
		Network:      &source.NetworkContext{Direct: true},
		HasSession:   true,
	}, nil
}

// Detach closes the DB and its session connection.
func (s *Source) Detach(_ context.Context, root string) error {
	c, err := decodeCursor(root)
	if err != nil {
		return err
	}
	s.mu.Lock()
	a := s.dbs[c.Root]
	delete(s.dbs, c.Root)
	s.mu.Unlock()
	if a == nil {
		return nil
	}
	a.sess.Close()
	return a.st.Close()
}

func (s *Source) lookup(sourceID string) (*attachment, cursor, error) {
	c, err := decodeCursor(sourceID)
	if err != nil {
		return nil, cursor{}, err
	}
	s.mu.Lock()
	a := s.dbs[c.Root]
	s.mu.Unlock()
	if a == nil {
		return nil, cursor{}, fmt.Errorf("%w: db not attached", source.ErrNotFound)
	}
	return a, c, nil
}

// List reads the underlying grid (auto-reconciling if it is itself
// source-backed in that DB) and maps each tile to a Node. A Gridwell grid
// read is the complete set, so the listing is authoritative.
func (s *Source) List(ctx context.Context, sourceID string) (source.Listing, error) {
	a, c, err := s.lookup(sourceID)
	if err != nil {
		return source.Listing{}, err
	}
	resp, err := a.st.GetGrid(ctx, c.Grid)
	if err != nil {
		return source.Listing{}, fmt.Errorf("%w: %v", source.ErrNotFound, err)
	}
	nodes := make([]source.Node, 0, len(resp.Tiles))
	for i := range resp.Tiles {
		nodes = append(nodes, nodeForTile(c, &resp.Tiles[i]))
	}
	return source.Listing{Nodes: nodes, Authoritative: true, Version: resp.Grid.Version}, nil
}

// Probe: the listing is authoritative, but implement presence anyway —
// present iff a tile with this key (its id) is still in the grid.
func (s *Source) Probe(ctx context.Context, sourceID, key string) (source.Presence, error) {
	a, c, err := s.lookup(sourceID)
	if err != nil {
		return source.PresenceUnknown, err
	}
	resp, err := a.st.GetGrid(ctx, c.Grid)
	if err != nil {
		return source.PresenceUnknown, nil
	}
	for i := range resp.Tiles {
		if strconv.FormatInt(resp.Tiles[i].ID, 10) == key {
			return source.PresencePresent, nil
		}
	}
	return source.PresenceGone, nil
}

// ReadBlob fetches a content blob by id. BlobRef is "blob:<id>" or
// "preview:<id>"; both resolve through the underlying blobs table.
func (s *Source) ReadBlob(ctx context.Context, sourceID, blobRef string) ([]byte, error) {
	a, _, err := s.lookup(sourceID)
	if err != nil {
		return nil, err
	}
	id, err := blobID(blobRef)
	if err != nil {
		return nil, err
	}
	return a.st.GetBlob(ctx, id)
}

// GetSession returns the DB's stored session blob (empty if none yet).
func (s *Source) GetSession(_ context.Context, root string) ([]byte, error) {
	a, _, err := s.lookup(root)
	if err != nil {
		return nil, err
	}
	var data []byte
	err = a.sess.QueryRow(`SELECT data FROM plugin_session WHERE id = 1`).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}
	return data, nil
}

// PutSession writes the DB's session blob back (single-row upsert).
func (s *Source) PutSession(_ context.Context, root string, data []byte) error {
	a, _, err := s.lookup(root)
	if err != nil {
		return err
	}
	_, err = a.sess.Exec(
		`INSERT INTO plugin_session (id, data) VALUES (1, ?)
		 ON CONFLICT(id) DO UPDATE SET data = excluded.data`, data)
	if err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	return nil
}

// Delete removes the underlying tile. Regular Gridwell tiles vanish
// immediately, so settled=true.
func (s *Source) Delete(ctx context.Context, sourceID, key string, version int64) (bool, error) {
	a, c, err := s.lookup(sourceID)
	if err != nil {
		return false, err
	}
	tileID, err := strconv.ParseInt(key, 10, 64)
	if err != nil {
		return false, fmt.Errorf("%w: bad key %q", source.ErrNotFound, key)
	}
	err = a.st.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path:    rpc.Path{WellIDs: c.Path},
		TileID:  tileID,
		Version: version,
	})
	if err != nil {
		return false, mapStoreErr(err)
	}
	return true, nil
}

// Move repositions / re-parents the underlying tile.
func (s *Source) Move(ctx context.Context, req source.MoveRequest) (source.Node, error) {
	a, c, err := s.lookup(req.SourceID)
	if err != nil {
		return source.Node{}, err
	}
	dst, err := decodeCursor(req.DestSourceID)
	if err != nil {
		return source.Node{}, err
	}
	tileID, err := strconv.ParseInt(req.Key, 10, 64)
	if err != nil {
		return source.Node{}, fmt.Errorf("%w: bad key %q", source.ErrNotFound, req.Key)
	}
	t, err := a.st.MoveTile(ctx, &rpc.MoveTileRequest{
		Path:       rpc.Path{WellIDs: c.Path},
		TileID:     tileID,
		Version:    req.Version,
		DestGridID: dst.Grid,
		DestPath:   rpc.Path{WellIDs: dst.Path},
		X:          req.X,
		Y:          req.Y,
	})
	if err != nil {
		return source.Node{}, mapStoreErr(err)
	}
	return nodeForTile(dst, t), nil
}

// Clone copies the underlying tile.
func (s *Source) Clone(ctx context.Context, req source.CloneRequest) (source.Node, error) {
	a, c, err := s.lookup(req.SourceID)
	if err != nil {
		return source.Node{}, err
	}
	dst, err := decodeCursor(req.DestSourceID)
	if err != nil {
		return source.Node{}, err
	}
	tileID, err := strconv.ParseInt(req.Key, 10, 64)
	if err != nil {
		return source.Node{}, fmt.Errorf("%w: bad key %q", source.ErrNotFound, req.Key)
	}
	t, err := a.st.CloneTile(ctx, &rpc.CloneTileRequest{
		Path:       rpc.Path{WellIDs: c.Path},
		TileID:     tileID,
		Version:    req.Version,
		DestGridID: dst.Grid,
		DestPath:   rpc.Path{WellIDs: dst.Path},
		X:          req.X,
		Y:          req.Y,
	})
	if err != nil {
		return source.Node{}, mapStoreErr(err)
	}
	return nodeForTile(dst, t), nil
}

// Write edits an underlying text tile's body.
func (s *Source) Write(ctx context.Context, sourceID, key string, version int64, data []byte) (source.Node, error) {
	a, c, err := s.lookup(sourceID)
	if err != nil {
		return source.Node{}, err
	}
	tileID, err := strconv.ParseInt(key, 10, 64)
	if err != nil {
		return source.Node{}, fmt.Errorf("%w: bad key %q", source.ErrNotFound, key)
	}
	t, err := a.st.UpdateText(ctx, &rpc.UpdateTextRequest{
		Path:    rpc.Path{WellIDs: c.Path},
		TileID:  tileID,
		Version: version,
		Data:    data,
	})
	if err != nil {
		return source.Node{}, mapStoreErr(err)
	}
	return nodeForTile(c, t), nil
}

// SetView forwards a well's framing or a text tile's scroll window.
func (s *Source) SetView(ctx context.Context, req source.SetViewRequest) (source.Node, error) {
	a, c, err := s.lookup(req.SourceID)
	if err != nil {
		return source.Node{}, err
	}
	tileID, err := strconv.ParseInt(req.Key, 10, 64)
	if err != nil {
		return source.Node{}, fmt.Errorf("%w: bad key %q", source.ErrNotFound, req.Key)
	}
	switch {
	case req.Frame != nil:
		t, err := a.st.SetWellView(ctx, &rpc.SetWellViewRequest{
			Path: rpc.Path{WellIDs: c.Path}, TileID: tileID, Version: req.Version,
			ViewX: req.Frame.ViewX, ViewY: req.Frame.ViewY, ViewZoom: req.Frame.ViewZoom,
		})
		if err != nil {
			return source.Node{}, mapStoreErr(err)
		}
		return nodeForTile(c, t), nil
	case req.Scroll != nil:
		t, err := a.st.SetTextView(ctx, &rpc.SetTextViewRequest{
			Path: rpc.Path{WellIDs: c.Path}, TileID: tileID, Version: req.Version,
			TextX: req.Scroll.X, TextY: req.Scroll.Y, TextW: req.Scroll.W, TextH: req.Scroll.H,
			TextMode: req.Scroll.Mode,
		})
		if err != nil {
			return source.Node{}, mapStoreErr(err)
		}
		return nodeForTile(c, t), nil
	default:
		return source.Node{}, fmt.Errorf("%w: SetView needs a frame or scroll", source.ErrUnsupported)
	}
}

// nodeForTile maps an underlying tile to a projected Node. parent is the
// cursor of the grid this tile lives in; a well's Child cursor descends
// one well-id deeper into the same DB.
func nodeForTile(parent cursor, t *rpc.Tile) source.Node {
	n := source.Node{
		Key:     strconv.FormatInt(t.ID, 10),
		Label:   t.AltText,
		X:       t.X,
		Y:       t.Y,
		W:       t.W,
		H:       t.H,
		Version: t.Version,
	}
	switch t.Kind {
	case rpc.KindWell:
		n.Kind = source.KindWell
		n.Child = encodeCursor(cursor{Root: parent.Root, Path: append(appendCopy(parent.Path), t.ID), Grid: t.ChildGridID})
		n.Frame = source.Frame{ViewX: t.ViewX, ViewY: t.ViewY, ViewZoom: t.ViewZoom}
		n.Caps = source.Caps{Delete: true, Move: true, Clone: true, Accept: true}
	case rpc.KindFileWell, rpc.KindProcessWell:
		// Descendable, but its children come from the underlying DB's own
		// host (fs / proc). Don't expose host-mutating caps two layers up.
		n.Kind = source.KindWell
		n.Child = encodeCursor(cursor{Root: parent.Root, Path: append(appendCopy(parent.Path), t.ID), Grid: t.ChildGridID})
		n.Frame = source.Frame{ViewX: t.ViewX, ViewY: t.ViewY, ViewZoom: t.ViewZoom}
	case rpc.KindText:
		n.Kind = source.KindText
		n.Body = &source.ContentRef{BlobRef: "blob:" + strconv.FormatInt(t.BlobID, 10), MediaType: "text/markdown"}
		n.TextView = source.Scroll{X: t.TextX, Y: t.TextY, W: t.TextW, H: t.TextH, Mode: t.TextMode}
		// Source-backed text (fs metadata, proc @info) is read-only.
		n.Caps = source.Caps{Delete: true, Move: true, Clone: true, Write: t.SourceKey == ""}
	case rpc.KindURL:
		n.Kind = source.KindURL
		n.URL = t.URLString
		n.Title = t.AltText
		if t.PreviewBlobID != 0 {
			n.Preview = &source.ContentRef{BlobRef: "preview:" + strconv.FormatInt(t.PreviewBlobID, 10), MediaType: "image/jpeg"}
		}
		n.Caps = source.Caps{Delete: true, Move: true, Clone: true}
	case rpc.KindShell:
		n.Kind = source.KindShell
		if t.PreviewBlobID != 0 {
			n.Preview = &source.ContentRef{BlobRef: "preview:" + strconv.FormatInt(t.PreviewBlobID, 10), MediaType: "image/jpeg"}
		}
		// A static DB hosts no live process: frozen preview only.
		n.Caps = source.Caps{Delete: true, Move: true}
	}
	return n
}

// appendCopy returns a fresh copy of path so concurrent Node mappings
// don't alias and clobber a shared backing array.
func appendCopy(path []int64) []int64 {
	out := make([]int64, len(path))
	copy(out, path)
	return out
}

func blobID(blobRef string) (int64, error) {
	s := blobRef
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[i+1:]
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: bad blob ref %q", source.ErrNotFound, blobRef)
	}
	return id, nil
}

// mapStoreErr translates the store's sentinel errors into the source
// package's so the host maps them uniformly regardless of plugin.
func mapStoreErr(err error) error {
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "version"):
		return fmt.Errorf("%w: %v", source.ErrConflict, err)
	default:
		return err
	}
}

func openSession(file string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", file)
	if err != nil {
		return nil, fmt.Errorf("open session db: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS plugin_session (
		id   INTEGER PRIMARY KEY CHECK (id = 1),
		data BLOB NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create session table: %w", err)
	}
	return db, nil
}
