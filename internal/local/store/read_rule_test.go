package store

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store/storetest"
)

// Reading never mutates: step out of a grid, look back, and the file is
// byte-identical. Only a read can break it silently — a write that stamps the
// wrong row shows up as the wrong picture, while a read that stamps one shows
// up as nothing at all until the user notices a thing they never touched has
// moved. The two tests below are the whole rule at this layer: the table names
// every read verb the store serves, and the snapshot is the file.
//
// The exception is stated, not hidden: the node's two singleton grids, scratch
// and trash, are materialized by the first read that needs their id. Which
// reads those are is pinned below, and so is the once-ness.

// readSubject is a store with one of everything a read can reach.
type readSubject struct {
	root, child      string
	text, url, shell string
	well, pane, link string
	trashed          string
	scratch, trash   string
	ns               *Namespace
	nsGrid, nsTile   int64
	entries          []*pluginv1.Entry
}

// seedForReads writes, through the product's own verbs, a tile of every kind,
// a nested grid, a capture, a framing, a trashed row, an ephemeral, and a
// plugin namespace with a minted row. Every read verb below has something to
// find, so a read that writes has something to write over.
func seedForReads(t *testing.T, s *Store) readSubject {
	t.Helper()
	ctx := context.Background()
	root := rootID(t, s)
	sub := readSubject{root: root}

	text, err := s.CreateText(ctx, root, 0, 0, 2, 2, []byte("# hello\n\nsome findable words"))
	if err != nil {
		t.Fatal(err)
	}
	sub.text = text.Id
	if _, err := s.SetTextView(ctx, text.Id, 3, 9, 300, 400, "rendered"); err != nil {
		t.Fatal(err)
	}

	url, err := s.CreateURL(ctx, root, 3, 0, 2, 2, "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	sub.url = url.Id
	if _, err := s.SetURLState(ctx, url.Id, []byte("jpegbytes"), "https://example.com/deep", "Example", `["https://example.com"]`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFrozen(ctx, url.Id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetContentZoom(ctx, url.Id, 1.5); err != nil {
		t.Fatal(err)
	}

	shell, err := s.CreateShell(ctx, root, 6, 0, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	sub.shell = shell.Id
	if _, err := s.SetShellPreview(ctx, shell.Id, []byte("shelljpeg")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTileAlt(ctx, shell.Id, "vim CLAUDE.md", false); err != nil {
		t.Fatal(err)
	}

	well, err := s.CreateWell(ctx, root, 0, 3, 2, 2, "a room")
	if err != nil {
		t.Fatal(err)
	}
	sub.well, sub.child = well.Id, well.ChildGridId
	if _, err := s.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		TileId: well.Id, Cx: 3, Cy: 4, Zoom: 1.25,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateText(ctx, well.ChildGridId, 1, 1, 1, 1, []byte("inside")); err != nil {
		t.Fatal(err)
	}

	pane, err := s.CreatePane(ctx, root, 3, 3, 2, 2, "ws", []byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	sub.pane = pane.Id

	link, err := s.CreateLeafLink(ctx, root, 6, 3, 1, 1, rpc.KindText, remoteTarget, "a link")
	if err != nil {
		t.Fatal(err)
	}
	sub.link = link.Id

	if _, err := s.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		RootGridId: root, Cx: -2, Cy: 7, Zoom: 0.5,
	}); err != nil {
		t.Fatal(err)
	}

	doomed, err := s.CreateText(ctx, root, 9, 0, 1, 1, []byte("bound for the trash"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: doomed.Id}); err != nil {
		t.Fatal(err)
	}
	sub.trashed = doomed.Id

	if _, err := s.CreateScratchURL(ctx, "https://visited.example"); err != nil {
		t.Fatal(err)
	}
	if sub.scratch, err = s.ScratchGridID(ctx); err != nil {
		t.Fatal(err)
	}
	if sub.trash, err = s.TrashGridID(ctx); err != nil {
		t.Fatal(err)
	}

	sub.ns = s.Namespace("plug1")
	gid, err := sub.ns.ContextID("a/context")
	if err != nil {
		t.Fatal(err)
	}
	sub.nsGrid = gid
	sub.entries = []*pluginv1.Entry{
		{Key: "notes.md", Kind: "text", Label: "notes.md"},
		{Key: "sub", Kind: "well", Label: "sub"},
	}
	id, err := sub.ns.Mint(gid, sub.entries[0], 0, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	sub.nsTile = id
	if err := sub.ns.SetFraming(0, gid, rpc.Framing{Cx: 1, Cy: 2, Zoom: 0.75}); err != nil {
		t.Fatal(err)
	}
	return sub
}

// readCase is one read verb. A read answers or fails; either way the file is
// the same afterwards, so the error is deliberately not checked here — a read
// that writes on the way to ErrNotFound is the same defect.
type readCase struct {
	name string
	run  func(ctx context.Context, s *Store, sub readSubject)
}

// readCases is every read the store serves, the wire's four (GetGrid, GetTile,
// GetTilePreview, Search) plus the handshake's fields, content, blobs and the
// plugin namespace's join.
func readCases() []readCase {
	ignore2 := func(_ any, _ error) {}
	return []readCase{
		{"RootGridID", func(ctx context.Context, s *Store, _ readSubject) { s.RootGridID(ctx) }},
		{"PluginUUID", func(ctx context.Context, s *Store, _ readSubject) { s.PluginUUID(ctx) }},
		{"ScratchGridID", func(ctx context.Context, s *Store, _ readSubject) { s.ScratchGridID(ctx) }},
		{"TrashGridID", func(ctx context.Context, s *Store, _ readSubject) { s.TrashGridID(ctx) }},
		{"RootFraming", func(ctx context.Context, s *Store, _ readSubject) { s.RootFraming(ctx) }},
		{"GridFraming/root", func(ctx context.Context, s *Store, sub readSubject) { s.GridFraming(sub.root) }},
		{"GridFraming/child", func(ctx context.Context, s *Store, sub readSubject) { s.GridFraming(sub.child) }},
		{"GetGrid/root", func(ctx context.Context, s *Store, sub readSubject) { ignore2(s.GetGrid(ctx, sub.root)) }},
		{"GetGrid/child", func(ctx context.Context, s *Store, sub readSubject) { ignore2(s.GetGrid(ctx, sub.child)) }},
		{"GetGrid/trash", func(ctx context.Context, s *Store, sub readSubject) { ignore2(s.GetGrid(ctx, sub.trash)) }},
		{"GetGrid/scratch", func(ctx context.Context, s *Store, sub readSubject) { ignore2(s.GetGrid(ctx, sub.scratch)) }},
		{"GetGrid/missing", func(ctx context.Context, s *Store, _ readSubject) { ignore2(s.GetGrid(ctx, "99999")) }},
		{"GetGrid/malformed", func(ctx context.Context, s *Store, _ readSubject) { ignore2(s.GetGrid(ctx, "not-an-id")) }},
		{"GetTile/text", func(ctx context.Context, s *Store, sub readSubject) { ignore2(s.GetTile(ctx, sub.text)) }},
		{"GetTile/url", func(ctx context.Context, s *Store, sub readSubject) { ignore2(s.GetTile(ctx, sub.url)) }},
		{"GetTile/shell", func(ctx context.Context, s *Store, sub readSubject) { ignore2(s.GetTile(ctx, sub.shell)) }},
		{"GetTile/well", func(ctx context.Context, s *Store, sub readSubject) { ignore2(s.GetTile(ctx, sub.well)) }},
		{"GetTile/pane", func(ctx context.Context, s *Store, sub readSubject) { ignore2(s.GetTile(ctx, sub.pane)) }},
		{"GetTile/link", func(ctx context.Context, s *Store, sub readSubject) { ignore2(s.GetTile(ctx, sub.link)) }},
		{"GetTile/trashed", func(ctx context.Context, s *Store, sub readSubject) { ignore2(s.GetTile(ctx, sub.trashed)) }},
		{"GetTile/missing", func(ctx context.Context, s *Store, _ readSubject) { ignore2(s.GetTile(ctx, "99999")) }},
		{"GetTilePreview/url", func(ctx context.Context, s *Store, sub readSubject) { ignore2(s.GetTilePreview(ctx, sub.url)) }},
		{"GetTilePreview/shell", func(ctx context.Context, s *Store, sub readSubject) { ignore2(s.GetTilePreview(ctx, sub.shell)) }},
		{"GetTilePreview/none", func(ctx context.Context, s *Store, sub readSubject) { ignore2(s.GetTilePreview(ctx, sub.text)) }},
		{"ReadContent/text", func(ctx context.Context, s *Store, sub readSubject) {
			s.ReadContent(ctx, sub.text)
		}},
		{"ReadContent/url", func(ctx context.Context, s *Store, sub readSubject) {
			s.ReadContent(ctx, sub.url)
		}},
		{"ReadContent/pane", func(ctx context.Context, s *Store, sub readSubject) {
			s.ReadContent(ctx, sub.pane)
		}},
		{"ReadContent/shell", func(ctx context.Context, s *Store, sub readSubject) {
			s.ReadContent(ctx, sub.shell)
		}},
		{"Search/text", func(ctx context.Context, s *Store, _ readSubject) { ignore2(s.Search(ctx, "findable", 20)) }},
		{"Search/name", func(ctx context.Context, s *Store, _ readSubject) { ignore2(s.Search(ctx, "a room", 20)) }},
		{"Search/id", func(ctx context.Context, s *Store, sub readSubject) { ignore2(s.Search(ctx, "id:"+sub.text, 20)) }},
		{"Search/nothing", func(ctx context.Context, s *Store, _ readSubject) { ignore2(s.Search(ctx, "zzzznomatch", 20)) }},
		{"ShellTileExists", func(ctx context.Context, s *Store, sub readSubject) { ignore2(s.ShellTileExists(ctx, sub.shell)) }},
		{"WorkspaceEphemeralRefs", func(ctx context.Context, s *Store, _ readSubject) {
			s.WorkspaceEphemeralRefs(ctx)
		}},
		{"GetBlob", func(ctx context.Context, s *Store, sub readSubject) {
			t, err := s.GetTile(ctx, sub.text)
			if err == nil {
				s.GetBlobWithMedia(ctx, t.BlobId)
				ignore2(s.GetBlob(ctx, t.BlobId))
			}
		}},
		{"Namespace/ContextKey", func(ctx context.Context, s *Store, sub readSubject) { ignore2(sub.ns.ContextKey(sub.nsGrid)) }},
		{"Namespace/LookupContext", func(ctx context.Context, s *Store, sub readSubject) { sub.ns.LookupContext("a/context") }},
		{"Namespace/LookupContext/absent", func(ctx context.Context, s *Store, sub readSubject) {
			sub.ns.LookupContext("never/touched")
		}},
		{"Namespace/TileKey", func(ctx context.Context, s *Store, sub readSubject) { sub.ns.TileKey(sub.nsTile) }},
		{"Namespace/LiveTileID", func(ctx context.Context, s *Store, sub readSubject) {
			sub.ns.LiveTileID(sub.nsGrid, "notes.md")
		}},
		{"Namespace/RootFraming", func(ctx context.Context, s *Store, sub readSubject) { sub.ns.RootFraming(sub.nsGrid) }},
		{"Namespace/Overlay", func(ctx context.Context, s *Store, sub readSubject) {
			ignore2(sub.ns.Overlay(sub.nsGrid, sub.entries))
		}},
		{"Namespace/Overlay/untouched context", func(ctx context.Context, s *Store, sub readSubject) {
			ignore2(sub.ns.Overlay(0, sub.entries))
		}},
	}
}

// Every read verb, against a store holding a tile of every kind, leaves the
// file byte-identical — the row it read, the rows it walked past, the ids it
// did not spend. The whole file is the oracle rather than the row the verb
// names, because a read that stamps the doorway it came through, or mints a
// row for an entry the user never touched, writes somewhere else.
func TestReadingNeverMutates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sub := seedForReads(t, s)

	for _, c := range readCases() {
		t.Run(c.name, func(t *testing.T) {
			before := storetest.Snapshot(t, s.SQL())
			c.run(ctx, s, sub)
			if after := storetest.Snapshot(t, s.SQL()); after != before {
				t.Errorf("%s wrote to the store:\n%s", c.name, storetest.Diff(before, after))
			}
		})
	}
}

// mintingReads are the reads that materialize one of the node's own singleton
// grids, scratch or trash, because they answer with its id and it has none
// until something asks. They are the whole exception to the rule above, and
// they spend an id once: the second call finds the row.
var mintingReads = map[string]bool{
	"ScratchGridID": true,
	"TrashGridID":   true,
	// Search filters the scratch grid out of its results, so it needs that id
	// before it can answer.
	"Search/text":    true,
	"Search/name":    true,
	"Search/nothing": true,
}

// On a home nothing has read yet, a read still writes nothing of the user's: a
// singleton grid appears with no tiles in it, once, and every other read leaves
// even that alone. A read that started minting a user row, or minting the same
// singleton twice, fails here.
func TestAReadMintsOnlyTheNodesOwnSingletons(t *testing.T) {
	minted := map[string]bool{}
	for _, c := range readCases() {
		t.Run(c.name, func(t *testing.T) {
			// A home nobody has opened yet: the bootstrapped root and nothing
			// else, so every row that appears was minted by this read. The ids
			// the case names are empty, which a read answers as absent, and a
			// read that writes on its way to absent is the same defect.
			s := newTestStore(t)
			ctx := context.Background()
			sub := readSubject{
				root:    rootID(t, s),
				ns:      s.Namespace("plug1"),
				entries: []*pluginv1.Entry{{Key: "notes.md", Kind: "text", Label: "notes.md"}},
			}

			tiles, blobs := storetest.Table(t, s.SQL(), "tiles"), storetest.Table(t, s.SQL(), "blobs")
			grids := storetest.Table(t, s.SQL(), "grids")
			c.run(ctx, s, sub)
			if got := storetest.Table(t, s.SQL(), "tiles"); got != tiles {
				t.Errorf("%s minted or changed a tile row", c.name)
			}
			if got := storetest.Table(t, s.SQL(), "blobs"); got != blobs {
				t.Errorf("%s wrote a blob", c.name)
			}
			if got := storetest.Table(t, s.SQL(), "grids"); got != grids {
				minted[c.name] = true
			}
			// Whatever it minted, it minted once.
			again := storetest.Snapshot(t, s.SQL())
			c.run(ctx, s, sub)
			if after := storetest.Snapshot(t, s.SQL()); after != again {
				t.Errorf("%s minted again on the second read:\n%s", c.name, storetest.Diff(again, after))
			}
		})
	}
	for name := range mintingReads {
		if !minted[name] {
			t.Errorf("%s no longer mints a singleton — the exception shrank; take it off the list", name)
		}
		delete(minted, name)
	}
	for name := range minted {
		t.Errorf("%s mints a grid on a fresh home and is not one of the node's singleton readers", name)
	}
}
