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

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/textedit"
)

type parityVariant struct {
	name    string
	declare func(*paritySource)
}

const parityReadOnly = "a summary stands in for the bytes"

var (
	vReadOnly    = parityVariant{"a read-only entry", func(s *paritySource) { s.grids["all"][0].ReadOnly = parityReadOnly }}
	vNotWritable = parityVariant{"a plugin that is not writable", func(s *paritySource) { s.unwritable = true }}
)

// The decisions the variants are held to, quoted.
const (
	dReadOnly = "so an edit is refused before it is typed"
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
