package server_test

// A variant is every paritySource of the fixture declaring one flag of
// rpc.Flags otherwise than its default, and its cells are the operations that
// flag changes: a same cell is held to home by the oracle, a decided one to
// its decision by holds, on the plugin the owner reaches (parityVariantNS).
// rpc.Flags names the variant as the flag's carrier.

import (
	"fmt"
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
	name    string
	declare func(*paritySource)
}

const (
	parityReadOnly = "a summary stands in for the bytes"
	parityCached   = "the source is not answering, so this is the cache"
)

var (
	vReadOnly    = parityVariant{"a read-only entry", func(s *paritySource) { s.grids["all"][0].ReadOnly = parityReadOnly }}
	vNotWritable = parityVariant{"a plugin that is not writable", func(s *paritySource) { s.unwritable = true }}
	vNoWatch     = parityVariant{"a plugin with watch off", func(s *paritySource) { s.unwatched = true }}
	vPartial     = parityVariant{"a listing that is not authoritative", func(s *paritySource) { s.partial = true }}
	vCached      = parityVariant{"a listing answered from cache", func(s *paritySource) { s.unreachable = parityCached }}
)

// The decisions the variants are held to, quoted.
const (
	dReadOnly = "so an edit is refused before it is typed"
	dLiveOff  = "a source with live updates off is read on every move"
	dCached   = "the read succeeds from what it caches and says so in `ListResponse.unreachable`"
)

type parityVariantCell struct {
	variant parityVariant
	parityCell
	holds func(f *parityFix, ns string) error // a decided cell's proof on the plugin ns
}

func (c parityVariantCell) String() string { return c.parityCell.String() + " on " + c.variant.name }

var parityVariantTable = []parityVariantCell{
	{vReadOnly, parityCell{kText, opWrite, onPlugin, decided, dReadOnly, nil}, holdsReadOnly(true)},
	{vReadOnly, parityCell{kText, opWrite, onFar, decided, dReadOnly, nil}, holdsReadOnly(true)},
	{vReadOnly, parityCell{kText, opDescend, onPlugin, same, "", checkDescend}, nil},
	{vNotWritable, parityCell{kText, opWrite, onPlugin, decided, dReadOnly, nil}, holdsReadOnly(false)},
	{vNotWritable, parityCell{kText, opWrite, onFar, decided, dReadOnly, nil}, holdsReadOnly(false)},
	{vNotWritable, parityCell{kURL, opWrite, onPlugin, same, "", checkWrite}, nil},
	{vNoWatch, parityCell{kURL, opUpdate, onPlugin, decided, dLiveOff, nil}, holdsLiveOff},
	{vPartial, parityCell{kText, opDead, onPlugin, same, "", checkDead}, nil},
	{vCached, parityCell{kText, opDead, onPlugin, decided, dCached, nil}, holdsCached},
	{vCached, parityCell{kText, opDead, onFar, decided, dCached, nil}, holdsCached},
}

// parityVariantNS is the plugin a decided cell's owner reaches: the local
// one, or for far the far node's through the connection.
var parityVariantNS = map[parityOwner]string{onPlugin: parityPluginUUID, onFar: parityConn + parityFarPluginUUID}

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
		if c.verdict == same && c.check != nil {
			t.Run(c.String(), func(t *testing.T) {
				t.Parallel()
				f := newParityFix(t, c.variant.declare)
				parityOracle(t, c.parityCell, c.check(f, onHome, c.kind), c.check(f, c.owner, c.kind))
			})
		}
		if c.verdict == decided && c.holds != nil {
			t.Run(c.String(), func(t *testing.T) {
				t.Parallel()
				f := newParityFix(t, c.variant.declare)
				if err := c.holds(f, parityVariantNS[c.owner]); err != nil {
					t.Error(err)
				}
			})
		}
	}
}

// entry is the row ns's plugin lists named name on its grid all.
func (f *parityFix) entry(ns, name string) (*gridwellv1.Grid, *gridwellv1.Tile, error) {
	g, err := f.cl.GetGrid(f.ctx, rpc.QualifyID(ns, rpc.EntryGridID("all")))
	if err != nil {
		return nil, nil, err
	}
	for _, tl := range g.Tiles {
		if tl.AltText == name {
			return g.Grid, tl, nil
		}
	}
	return nil, nil, fmt.Errorf("the plugin lists no %q", name)
}

// holdsReadOnly proves the text's edit is refused before it is typed, the
// client's verdict reading the row's sentence (entry) or the grid's stamp
// (!entry), and that a write sent anyway, as an agent's, is refused and
// leaves the body be.
func holdsReadOnly(entry bool) func(f *parityFix, ns string) error {
	return func(f *parityFix, ns string) error {
		g, tl, err := f.entry(ns, "Body")
		if err != nil {
			return err
		}
		if entry && (tl.ReadOnly != parityReadOnly || !g.Writable) || !entry && (tl.ReadOnly != "" || g.Writable) {
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
		if clientsync.Of(err) != clientsync.OutcomeRejected || entry && !strings.Contains(fmt.Sprint(err), parityReadOnly) {
			return fmt.Errorf("a write sent anyway answered %v, want refused with the reason", err)
		}
		if data, _, _, err := f.cl.ReadContent(f.ctx, tl.Id); err != nil || string(data) != "Body" {
			return fmt.Errorf("the body reads %q, %v after the refusal", data, err)
		}
		return nil
	}
}

// awaitHealth waits for the last health the source serving gridID told to
// pass ok, as the client's cache folds it.
func (f *parityFix) awaitHealth(gridID string, ok func(*cache.Cache, *gridwellv1.EventPluginHealth) bool) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		c := cache.New()
		var last *gridwellv1.EventPluginHealth
		f.healthMu.Lock()
		for uuid, h := range f.health {
			c.NoteHealth(uuid, h.Healthy)
			c.NoteLiveUpdates(uuid, h.GetLiveUpdatesOff() == "")
			if cache.ServedBy(gridID, uuid) {
				last = h
			}
		}
		f.healthMu.Unlock()
		if g, err := f.cl.GetGrid(f.ctx, gridID); err == nil {
			c.PutGrid(g.Grid, g.Tiles)
		}
		if last != nil && ok(c, last) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the source serving %s last told %+v", gridID, last)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// holdsLiveOff proves a source that cannot tell says so, so the client's
// cache holds none of its grids current and a move reads the change.
func holdsLiveOff(f *parityFix, ns string) error {
	g, tl, err := f.entry(ns, "Site")
	if err != nil {
		return err
	}
	if err := f.awaitHealth(g.Id, func(c *cache.Cache, h *gridwellv1.EventPluginHealth) bool {
		return h.Healthy && h.GetLiveUpdatesOff() != "" && !c.Current()[g.Id]
	}); err != nil {
		return fmt.Errorf("live updates off is never said: %v", err)
	}
	_, key, _, _ := rpc.SplitEntryID(rpc.LocalOf(tl.Id))
	f.src[ns].edit(key, parityEdits[kURL])
	_, after, err := f.entry(ns, "Site")
	if err != nil || after.UrlString != parityEdits[kURL] {
		return fmt.Errorf("the read on a move answers %v, %v", after, err)
	}
	return nil
}

// holdsCached proves a listing from the plugin's cache serves its rows,
// tells the source's health with the reason, and retires nothing, whatever
// authority it claims: a link to an entry the cached listing leaves out
// never reads dead.
func holdsCached(f *parityFix, ns string) error {
	g, tl, err := f.entry(ns, "Body")
	if err != nil {
		return err
	}
	if err := f.awaitHealth(g.Id, func(c *cache.Cache, h *gridwellv1.EventPluginHealth) bool {
		return !h.Healthy && h.Detail == parityCached && c.SourceDark(g.Id)
	}); err != nil {
		return fmt.Errorf("the cache is never told as the source's health: %v", err)
	}
	link, err := f.cl.CreateTile(f.ctx, &gridwellv1.CreateTileRequest{GridId: f.root[onHome],
		Tile: &gridwellv1.Tile{Kind: tl.Kind, X: 0, Y: 7, W: 1, H: 1, LinkTargetId: tl.Id}})
	if err != nil {
		return err
	}
	f.src[ns].remove(rpc.LocalOf(tl.Id))
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if _, err := f.cl.GetGrid(f.ctx, g.Id); err != nil {
			return fmt.Errorf("the cached listing answered %v", err)
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
