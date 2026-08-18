// Package instpick holds the pure decisions behind the parameterized-plugin
// instance picker (issue #251): which entries a plugin's instance grid
// yields, how they sort, what a row's summary says, when a form submit is a
// DUPLICATE of an existing instance (select it, don't mint a twin), and
// where a new instance well lands on the instance grid. The wasm picker
// modal is a thin renderer over these — charter §5: the decision logic is
// js-free and unit-tested; the wasm file contributes pixels and plumbing.
//
// The dedup here is client UX (pre-matching a submit to an existing entry);
// the plugin's refusal at commit is the authority, exactly as with schema
// validation.
package instpick

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/schemaform"
)

// Status classifies what the picker can do with an entry.
type Status int

const (
	// Ready: params committed and the instance's chain is known — the entry
	// can be adopted into a well or descended into.
	Ready Status = iota
	// Pending: params committed but the chain isn't learned yet (e.g. an
	// ssh connection whose remote hasn't answered its first Info). Shown as
	// connecting; deletable, not yet selectable.
	Pending
	// Inert: no params ever committed (a legal #209 leftover). Deletable
	// only.
	Inert
)

// Entry is one instance on the plugin's instance grid, as the picker sees
// it: the well tile's identity plus its params document.
type Entry struct {
	TileID      string
	Version     int64
	Name        string // alt_text — the connection's name
	ParamsJSON  string // "" = never configured
	ChildGridID string // "" until the chain is learned
	Detail      string // the plugin's last failure (Tile.StatusDetail); "" = none
	ViewX       int64
	ViewY       int64
	ViewZoom    float64
}

// Status derives the entry's picker state from the two facts it carries.
func (e Entry) Status() Status {
	switch {
	case e.ParamsJSON == "":
		return Inert
	case e.ChildGridID == "":
		return Pending
	}
	return Ready
}

// PendingLabel is a Pending row's status suffix. It carries the plugin's
// recorded failure when there is one — the row is exactly where someone
// stares when a connection won't come up, so the reason lives there too.
func (e Entry) PendingLabel() string {
	if e.Detail != "" {
		return "  (connecting… — " + e.Detail + ")"
	}
	return "  (connecting…)"
}

// BuildEntries turns an instance grid's tiles into the picker's ordered
// list: wells only, named entries first in case-insensitive name order,
// unnamed last by tile id (stable across opens). params returns the tile's
// fetched content document ("" when there is none).
func BuildEntries(tiles []rpc.Tile, params func(tileID string) string) []Entry {
	var out []Entry
	for _, t := range tiles {
		if t.Kind != rpc.KindWell {
			continue
		}
		out = append(out, Entry{
			TileID:      t.ID,
			Version:     t.Version,
			Name:        t.AltText,
			ParamsJSON:  params(t.ID),
			ChildGridID: t.ChildGridID,
			Detail:      t.StatusDetail,
			ViewX:       t.ViewX,
			ViewY:       t.ViewY,
			ViewZoom:    t.ViewZoom,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Name == "") != (b.Name == "") {
			return b.Name == "" // named before unnamed
		}
		an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if an != bn {
			return an < bn
		}
		return a.TileID < b.TileID
	})
	return out
}

// Summary renders an entry's non-secret params for its row, in the form's
// field order (schemaform sorts by name — JSON schemas carry no declaration
// order): "host=gpu.example user=joe". Secret fields (key paths) never
// show; empty fields are skipped. Unparseable params read as "" — the row
// still shows its name and status.
func Summary(form *schemaform.Form, paramsJSON string) string {
	if form == nil || paramsJSON == "" {
		return ""
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(paramsJSON), &doc); err != nil {
		return ""
	}
	var parts []string
	for _, fd := range form.Fields {
		if fd.Secret {
			continue
		}
		v, ok := doc[fd.Name]
		if !ok {
			continue
		}
		s := valueString(v)
		if s == "" {
			continue
		}
		parts = append(parts, fd.Name+"="+s)
	}
	return strings.Join(parts, " ")
}

func valueString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return ""
}

// Match finds the existing entry whose params document equals the submitted
// one — "selecting an existing entry means the same connection, not a copy"
// (owner decision 2026-08-08), so a submit that names identical details
// reuses the instance instead of minting a twin. Equality is canonical:
// key order is irrelevant and empty values count as absent. Returns nil
// when nothing matches.
func Match(entries []Entry, paramsJSON []byte) *Entry {
	want, ok := canonical(string(paramsJSON))
	if !ok {
		return nil
	}
	for i := range entries {
		if entries[i].ParamsJSON == "" {
			continue
		}
		if got, ok := canonical(entries[i].ParamsJSON); ok && got == want {
			return &entries[i]
		}
	}
	return nil
}

// canonical reduces a params document to a comparable form: parsed, empty
// strings dropped, keys sorted (json.Marshal of a map sorts keys).
func canonical(doc string) (string, bool) {
	var m map[string]any
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		return "", false
	}
	for k, v := range m {
		if s, isStr := v.(string); isStr && s == "" {
			delete(m, k)
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// FreeCell picks where a new instance well lands on the instance grid: one
// column right of the rightmost existing tile, on its row (row 0 on an
// empty grid). The instance grid is storage, not a place — the position
// only has to be free, and past-the-end always is.
func FreeCell(tiles []rpc.Tile) (x, y int64) {
	first := true
	for _, t := range tiles {
		if first || t.X+t.W > x {
			x = t.X + t.W
			y = t.Y
			first = false
		}
	}
	return x, y
}
