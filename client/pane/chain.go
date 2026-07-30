package pane

import "slices"

// Crumb is one level of a pane's vertical location, root inclusive: the
// bottom bar renders one square per crumb and clicking a crumb ascends the
// pane back to that level (issue #212). The chain is DERIVED from the pane's
// own facts — Up frames, Anchor, Path, TextFocus — never stored, so it can
// not drift from the place it describes (charter §1).
//
// Exactly one of Anchor (a root crumb: the qualified root grid id of a
// namespace level) or TileID (a descended tile) is set. UpLen/PathLen/
// HasText describe the pane-state SHAPE when this crumb is the innermost
// level — the ascent loop's stop condition. ParentAnchor/ParentPath locate
// a tile crumb's row (the grid it sits in) for preview and label rendering.
type Crumb struct {
	Anchor string
	TileID string
	Text   bool

	UpLen   int
	PathLen int
	HasText bool

	ParentAnchor string
	ParentPath   []string
}

// DescentChain returns the pane's vertical location as crumbs, outermost
// first: for each portal frame its root and descended tiles, then the
// current level's root, path wells, and text descent. The last crumb is
// where the pane is now; a boot-blank pane (empty anchor) has none.
func DescentChain(p *Pane) []Crumb {
	if p == nil || (p.Anchor == "" && len(p.Up) == 0) {
		return nil
	}
	var out []Crumb
	level := func(anchor string, path []string, textFocus string, upLen int) {
		out = append(out, Crumb{Anchor: anchor, UpLen: upLen, ParentAnchor: anchor})
		for i, id := range path {
			out = append(out, Crumb{TileID: id, UpLen: upLen, PathLen: i + 1,
				ParentAnchor: anchor, ParentPath: slices.Clone(path[:i])})
		}
		if textFocus != "" {
			out = append(out, Crumb{TileID: textFocus, Text: true, UpLen: upLen,
				PathLen: len(path), HasText: true,
				ParentAnchor: anchor, ParentPath: slices.Clone(path)})
		}
	}
	for i, f := range p.Up {
		level(f.Anchor, f.Path, f.TextFocus, i)
	}
	level(p.Anchor, p.Path, p.TextFocus, len(p.Up))
	return out
}

// depthKey orders pane states by descent depth: portal frames dominate,
// then in-namespace path length, then a leaf text descent. Each single
// ascent (text, path pop, frame pop) strictly decreases it.
func depthKey(upLen, pathLen int, hasText bool) [3]int {
	t := 0
	if hasText {
		t = 1
	}
	return [3]int{upLen, pathLen, t}
}

// DeeperThan reports whether pane p is strictly deeper than crumb c's
// level — the ascent loop's continue condition.
func DeeperThan(p *Pane, c Crumb) bool {
	pk := depthKey(len(p.Up), len(p.Path), p.TextFocus != "")
	ck := depthKey(c.UpLen, c.PathLen, c.HasText)
	for i := range pk {
		if pk[i] != ck[i] {
			return pk[i] > ck[i]
		}
	}
	return false
}

// OneAscentReaches reports whether a single ascent from p's current state
// lands exactly on crumb c — the point where the multi-level jump hands
// the final hop to the ordinary animated ascent.
func OneAscentReaches(p *Pane, c Crumb) bool {
	switch {
	case p.TextFocus != "":
		return c.UpLen == len(p.Up) && c.PathLen == len(p.Path) && !c.HasText
	case len(p.Path) > 0:
		return c.UpLen == len(p.Up) && c.PathLen == len(p.Path)-1 && !c.HasText
	case len(p.Up) > 0:
		f := p.Up[len(p.Up)-1]
		return c.UpLen == len(p.Up)-1 && c.PathLen == len(f.Path) &&
			c.HasText == (f.TextFocus != "")
	}
	return false
}
