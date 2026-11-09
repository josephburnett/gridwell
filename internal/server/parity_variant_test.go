package server_test

// A variant is a state of every node's files otherwise than the fixture's,
// and its cells are the operations that state changes: a same cell is held
// to home by the oracle, a decided one to its decision by holds, on the
// files the owner reaches (parityVariantPrefix). rpc.Flags names the variant
// as the flag's carrier.

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/textedit"
)

type parityVariant struct {
	name string
	// apply puts the state once the fixture is built; nil when holds puts
	// it, since the tiles it needs must be made first.
	apply func(f *parityFix)
}

var (
	vReadOnly = parityVariant{"a read-only file", func(f *parityFix) {
		for _, d := range f.disk {
			if err := os.Chmod(filepath.Join(d.main, "Body.md"), 0o444); err != nil {
				f.t.Fatal(err)
			}
		}
	}}
	vDark     = parityVariant{"a directory that will not read", nil}
	vRefused  = parityVariant{"a directory the OS refuses", nil}
	vDisabled = parityVariant{"a disabled source", nil}
	vUntold   = parityVariant{"a far node refusing the share", func(f *parityFix) { f.h.refuseShare.Store(true) }}
	vInfo     = parityVariant{"a file that renders as nothing", func(f *parityFix) {
		for _, d := range f.disk {
			writeFile(f.t, filepath.Join(d.main, "Blob.bin"), "\x1f\x8bBlob")
		}
	}}
)

// The decisions the variants are held to, quoted.
const (
	dReadOnly      = "a file the node cannot write is read-only with the reason"
	dDark          = "its grid serves what its rows hold and says so through the health event"
	dRefused       = "is refused with the reason on its grid, and on its door once a"
	dDisabled      = "every grid it serves is stamped `writable=false`, `accepts_tiles=false`, each row `read_only` with the reason, so an edit is refused before it is typed"
	dNeverDisabled = "The files are dark or told, never disabled"
	dUntold        = "a source whose changes are not told is read on every move"
	dInfo          = "a file whose name renders as nothing, by the one table of what renders as text, is a text tile with no body that shows its file information"
	dBytes         = "and every other file's bytes as `application/octet-stream`, which no browser runs"
)

type parityVariantCell struct {
	variant parityVariant
	parityCell
	holds func(f *parityFix, prefix string) error // a decided cell's proof on the files reached through prefix
}

func (c parityVariantCell) String() string { return c.parityCell.String() + " on " + c.variant.name }

var parityVariantTable = []parityVariantCell{
	{vReadOnly, parityCell{kText, opWrite, onFiles, decided, dReadOnly, nil}, holdsReadOnly},
	{vReadOnly, parityCell{kText, opWrite, onFar, decided, dReadOnly, nil}, holdsReadOnly},
	{vReadOnly, parityCell{kText, opDescend, onFiles, same, "", checkDescend}, nil},
	{vDark, parityCell{kText, opDead, onFiles, decided, dDark, nil}, holdsDark},
	{vDark, parityCell{kText, opDead, onFar, decided, dDark, nil}, holdsDark},
	{vRefused, parityCell{kDoorDir, opDescend, onFiles, decided, dRefused, nil}, holdsRefused},
	{vRefused, parityCell{kDoorDir, opDescend, onFar, decided, dRefused, nil}, holdsRefused},
	{vDisabled, parityCell{kText, opWrite, onFar, decided, dDisabled, nil}, holdsDisabled},
	{vDisabled, parityCell{kText, opWrite, onFiles, decided, dNeverDisabled, nil}, holdsNeverDisabled},
	{vUntold, parityCell{kURL, opUpdate, onFar, decided, dUntold, nil}, holdsUntold},
	{vInfo, parityCell{kText, opRead, onFiles, decided, dInfo, nil}, holdsInfo},
	{vInfo, parityCell{kText, opRead, onFar, decided, dInfo, nil}, holdsInfo},
	{vInfo, parityCell{kText, opServe, onFiles, decided, dBytes, nil}, holdsBytes},
	{vInfo, parityCell{kText, opServe, onFar, decided, dBytes, nil}, holdsBytes},
}

// parityVariantPrefix is the node whose files a decided cell's owner
// reaches: the local one, or for far the far node's through the connection.
var parityVariantPrefix = map[parityOwner]string{onFiles: "", onFar: parityConn}

// parityVariantRows are the variant cells as rows of the table, each named
// for its variant, so checkParityTable holds them as it holds the rest.
func parityVariantRows() []parityCell {
	out := make([]parityCell, 0, len(parityVariantTable))
	for _, c := range parityVariantTable {
		row := c.parityCell
		row.kind = parityKind(fmt.Sprintf("%s on %s", row.kind, c.variant.name))
		out = append(out, row)
	}
	return out
}

// parityVariantOwed is the variant cells' part of the count: every hole, and
// every cell with no check or proof for its verdict.
func parityVariantOwed() int {
	n := 0
	for _, c := range parityVariantTable {
		if c.verdict == hole || c.verdict == same && c.check == nil || c.verdict == decided && c.holds == nil {
			n++
		}
	}
	return n
}

func runParityVariants(t *testing.T) {
	for _, c := range parityVariantTable {
		var vs []parityVariant
		if c.variant.apply != nil {
			vs = append(vs, c.variant)
		}
		if c.verdict == same && c.check != nil {
			t.Run(c.String(), func(t *testing.T) {
				t.Parallel()
				f := newParityFix(t, vs...)
				parityOracle(t, c.parityCell, c.check(f, onHome, c.kind), c.check(f, c.owner, c.kind))
			})
		}
		if c.verdict == decided && c.holds != nil {
			t.Run(c.String(), func(t *testing.T) {
				t.Parallel()
				f := newParityFix(t, vs...)
				if err := c.holds(f, parityVariantPrefix[c.owner]); err != nil {
					t.Error(err)
				}
			})
		}
	}
}

// body is the text file Body.md on main of the files reached through prefix,
// and the grid listing it.
func (f *parityFix) body(prefix string) (*gridwellv1.Grid, *gridwellv1.Tile, error) {
	g, err := f.cl.GetGrid(f.ctx, f.fileID(prefix, f.disk[prefix].main))
	if err != nil {
		return nil, nil, err
	}
	for _, tl := range g.Tiles {
		if tl.AltText == "Body.md" {
			return g.Grid, tl, nil
		}
	}
	return nil, nil, fmt.Errorf("the files list no Body.md")
}

// holdsReadOnly proves the text's edit is refused before it is typed, the
// client's verdict reading the row's sentence, and that a write sent anyway,
// as an agent's, is refused with that sentence and leaves the body be.
func holdsReadOnly(f *parityFix, prefix string) error {
	g, tl, err := f.body(prefix)
	if err != nil {
		return err
	}
	if tl.ReadOnly == "" || !g.Writable {
		return fmt.Errorf("the row says read_only %q on a grid writable=%v", tl.ReadOnly, g.Writable)
	}
	if !textedit.ReadOnly(tl, g.Writable) {
		return fmt.Errorf("the client offers an edit")
	}
	_, _, basis, err := f.cl.ReadContent(f.ctx, tl.Id)
	if err != nil {
		return err
	}
	_, err = f.cl.WriteContent(f.ctx, tl.Id, basis, []byte("Body edited"))
	if clientsync.Of(err) != clientsync.OutcomeRejected || !strings.Contains(fmt.Sprint(err), tl.ReadOnly) {
		return fmt.Errorf("a write sent anyway answered %v, want refused with %q", err, tl.ReadOnly)
	}
	if data, _, _, err := f.cl.ReadContent(f.ctx, tl.Id); err != nil || string(data) != "Body.md" {
		return fmt.Errorf("the body reads %q, %v after the refusal", data, err)
	}
	return nil
}

// blob is the file Blob.bin on main of the files reached through prefix.
func (f *parityFix) blob(prefix string) (*gridwellv1.Tile, error) {
	g, err := f.cl.GetGrid(f.ctx, f.fileID(prefix, f.disk[prefix].main))
	if err != nil {
		return nil, err
	}
	if i := slices.IndexFunc(g.Tiles, func(tl *gridwellv1.Tile) bool { return tl.AltText == "Blob.bin" }); i >= 0 {
		return g.Tiles[i], nil
	}
	return nil, fmt.Errorf("the files list no Blob.bin")
}

// holdsInfo proves a file that renders as nothing is listed as a text tile
// shown as its file information, read-only, whose read is no bytes and whose
// info read is the disk's facts, the sniffed type among them.
func holdsInfo(f *parityFix, prefix string) error {
	tl, err := f.blob(prefix)
	if err != nil {
		return err
	}
	if tl.Kind != rpc.KindText || tl.TextPresentation != rpc.TextPresentationInfo || tl.ReadOnly == "" {
		return fmt.Errorf("Blob.bin is kind %q presentation %q read_only %q", tl.Kind, tl.TextPresentation, tl.ReadOnly)
	}
	if data, _, _, err := f.cl.ReadContent(f.ctx, tl.Id); err != nil || len(data) != 0 {
		return fmt.Errorf("its read answered %q, %v, want no bytes", data, err)
	}
	info, err := f.cl.ReadInfo(f.ctx, tl.Id)
	if err != nil || info.GetSize() != 6 || info.GetSniffedType() != "gzip" || info.GetExtensionType() != "binary" {
		return fmt.Errorf("its info read answered %v, %v", info, err)
	}
	return nil
}

// holdsBytes proves the content API serves a file that renders as nothing
// as octet-stream, sniffed by no browser, one range at a time.
func holdsBytes(f *parityFix, prefix string) error {
	tl, err := f.blob(prefix)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, rpc.PageURL(f.h.localURL, f.token, tl.Id), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", "bytes=2-5")
	resp, err := f.h.localHTTP.Client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	h := resp.Header
	if err != nil || resp.StatusCode != http.StatusPartialContent || string(body) != "Blob" ||
		h.Get("Content-Type") != "application/octet-stream" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Content-Range") != "bytes 2-5/6" {
		return fmt.Errorf("the range answered %d %q %v with %v", resp.StatusCode, body, err, h)
	}
	return nil
}

// awaitHealth waits for the last health the source serving gridID told to
// pass ok, given the client's cache with every health told folded in.
func (f *parityFix) awaitHealth(gridID string, ok func(*cache.Cache, *gridwellv1.EventSourceHealth) bool) error {
	deadline := time.Now().Add(10 * time.Second)
	f.heard.NoteStream(true)
	for {
		if g, err := f.cl.GetGrid(f.ctx, gridID); err == nil {
			f.heard.PutGrid(g.Grid, g.Tiles)
		}
		var last *gridwellv1.EventSourceHealth
		f.healthMu.Lock()
		for uuid, h := range f.health {
			if cache.ServedBy(gridID, uuid) {
				last = h
			}
		}
		passed := last != nil && ok(f.heard, last)
		f.healthMu.Unlock()
		if passed {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the source serving %s last told %+v", gridID, last)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// holdsDark proves a directory that will not read serves the rows the node
// holds, tells the files' health with the reason, and retires nothing: a
// link to a file taken out while it is dark never reads dead.
func holdsDark(f *parityFix, prefix string) error {
	g, tl, err := f.body(prefix)
	if err != nil {
		return err
	}
	// A touch mints the row a dark directory still serves.
	if _, err := f.cl.SetContentZoom(f.ctx, tl.Id, 1.5); err != nil {
		return err
	}
	link, err := f.cl.CreateTile(f.ctx, &gridwellv1.CreateTileRequest{GridId: f.root[onHome],
		Tile: &gridwellv1.Tile{Kind: tl.Kind, X: 0, Y: 7, W: 1, H: 1, LinkTargetId: tl.Id}})
	if err != nil {
		return err
	}
	main := f.disk[prefix].main
	unanswering(f.t, main)
	if err := os.Remove(filepath.Join(main+".away", "Body.md")); err != nil {
		return err
	}
	if err := f.awaitHealth(g.Id, func(c *cache.Cache, h *gridwellv1.EventSourceHealth) bool {
		return !h.Healthy && strings.Contains(h.Detail, main) && c.SourceDark(g.Id)
	}); err != nil {
		return fmt.Errorf("the dark directory is never told as the files' health: %v", err)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		dark, err := f.cl.GetGrid(f.ctx, g.Id)
		if err != nil {
			return fmt.Errorf("the dark directory answered %v", err)
		}
		if len(dark.Tiles) != 1 || dark.Tiles[0].Id != tl.Id {
			return fmt.Errorf("the dark directory serves %v, want the one row the node holds", dark.Tiles)
		}
		row, err := f.cl.GetTile(f.ctx, link.Id)
		if err != nil {
			return err
		}
		if d := f.draw(row); !strings.HasPrefix(d, "dead=false") {
			return fmt.Errorf("the link draws %s", d)
		}
	}
	return nil
}

// holdsRefused proves a directory the OS refuses this user is a verdict at
// every hop: a read of its grid is refused with the reason, its door then
// says why, and the files' health never goes dark, though a session shows it.
func holdsRefused(f *parityFix, prefix string) error {
	if os.Geteuid() == 0 {
		f.t.Skip("root reads every directory")
	}
	main := f.disk[prefix].main
	door := filepath.Join(main, "Door")
	if err := os.Chmod(door, 0); err != nil {
		return err
	}
	f.t.Cleanup(func() { _ = os.Chmod(door, 0o755) })
	g, err := f.cl.GetGrid(f.ctx, f.fileID(prefix, main))
	if err != nil {
		return err
	}
	i := slices.IndexFunc(g.Tiles, func(tl *gridwellv1.Tile) bool { return tl.AltText == "Door" })
	if i < 0 {
		return fmt.Errorf("the files list no Door")
	}
	_, err = f.cl.GetGrid(f.ctx, g.Tiles[i].ChildGridId)
	if clientsync.Of(err) != clientsync.OutcomeRejected || !strings.Contains(fmt.Sprint(err), "cannot be read") {
		return fmt.Errorf("the refused grid answered %v, want refused with the reason", err)
	}
	says := ""
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && !strings.Contains(says, "Door cannot be read"); time.Sleep(50 * time.Millisecond) {
		if g, err = f.cl.GetGrid(f.ctx, g.Grid.Id); err != nil {
			return err
		}
		says = g.Tiles[i].ReadOnly
	}
	if !strings.Contains(says, "Door cannot be read") {
		return fmt.Errorf("the refused door says read_only %q after the refusal", says)
	}
	time.Sleep(300 * time.Millisecond)
	f.healthMu.Lock()
	defer f.healthMu.Unlock()
	for uuid, h := range f.health {
		if cache.ServedBy(g.Grid.Id, uuid) && !h.Healthy {
			return fmt.Errorf("the refused directory was told as the files' health: %+v", h)
		}
	}
	return nil
}

// holdsDisabled proves a far home's text, its connection disabled, is
// refused an edit before it is typed, the grid stamped taking no edit, no
// tile and no move and the row carrying the reason, and that a write sent
// anyway is the disabled verdict and lands nothing on the far node.
func holdsDisabled(f *parityFix, _ string) error {
	tl := f.tile(onFar, kText)
	if err := f.cl.DisableSource(f.ctx, strings.TrimSuffix(parityConn, "/")); err != nil {
		return err
	}
	g, err := f.cl.GetGrid(f.ctx, f.root[onFar])
	if err != nil {
		return err
	}
	var row *gridwellv1.Tile
	for _, r := range g.Tiles {
		if r.Id == tl.Id {
			row = r
		}
	}
	switch {
	case row == nil:
		return fmt.Errorf("the disabled far home no longer lists %s", tl.Id)
	case g.Grid.Writable || g.Grid.GetAcceptsTiles() || g.Grid.GetMoves() || row.ReadOnly == "":
		return fmt.Errorf("the grid says writable=%v accepts_tiles=%v moves=%v and the row read_only %q",
			g.Grid.Writable, g.Grid.GetAcceptsTiles(), g.Grid.GetMoves(), row.ReadOnly)
	case !textedit.ReadOnly(row, g.Grid.Writable):
		return fmt.Errorf("the client offers an edit")
	}
	_, err = f.cl.WriteContent(f.ctx, tl.Id, rpc.BasisOf(tl), []byte("Body edited"))
	if clientsync.Of(err) != clientsync.OutcomeDisabled {
		return fmt.Errorf("a write sent anyway answered %v, want the disabled verdict", err)
	}
	n, id := f.holder(tl.Id)
	if data, _, _, err := n.cl.ReadContent(f.ctx, id); err != nil || string(data) != "Body.md" {
		return fmt.Errorf("the far body reads %q, %v after the refusal", data, err)
	}
	return nil
}

// holdsNeverDisabled proves the files have no switch: a disable of them is
// refused, their grid stays writable, and a text write lands.
func holdsNeverDisabled(f *parityFix, _ string) error {
	if err := f.cl.DisableSource(f.ctx, rpc.FilesOwner(localNodeID)); err == nil || !strings.Contains(err.Error(), rpc.FilesOwner(localNodeID)) {
		return fmt.Errorf("a disable of the files answered %v, want refused naming them", err)
	}
	g, tl, err := f.body("")
	if err != nil {
		return err
	}
	if !g.Writable || tl.ReadOnly != "" {
		return fmt.Errorf("the files say writable=%v and read_only %q after the refused disable", g.Writable, tl.ReadOnly)
	}
	_, _, basis, err := f.cl.ReadContent(f.ctx, tl.Id)
	if err != nil {
		return err
	}
	if _, err := f.cl.WriteContent(f.ctx, tl.Id, basis, []byte("Body edited")); err != nil {
		return fmt.Errorf("a text write after the refused disable answered %v", err)
	}
	return nil
}

// holdsUntold proves a connection whose share the far node refused says its
// changes are not told, with the failure, so the client's cache holds none
// of its grids current, and a read on a move answers a far change.
func holdsUntold(f *parityFix, _ string) error {
	grid := f.root[onFar]
	tl := f.tile(onFar, kURL)
	if err := f.awaitHealth(grid, func(c *cache.Cache, h *gridwellv1.EventSourceHealth) bool {
		return h.Healthy && h.Told != nil && !h.GetTold() && h.LiveUpdatesOff != "" && !c.Current()[grid]
	}); err != nil {
		return fmt.Errorf("that its changes are not told is never said: %v", err)
	}
	f.change(tl)
	after, err := f.cl.GetTile(f.ctx, tl.Id)
	if err != nil || after.UrlString != parityEdits[kURL] {
		return fmt.Errorf("the read on a move answers %v, %v", after, err)
	}
	return nil
}
