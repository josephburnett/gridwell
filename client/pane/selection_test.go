package pane

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// The selection a pane draws is its grid row's at home and the layout's inside
// a pane tile; a content place has none.
func TestSelectionTarget(t *testing.T) {
	grid := NewStack("g7abcde")
	content := NewStack("g7abcde")
	content.Push(Frame{Door: "t7abcde", Content: true})
	tests := []struct {
		name       string
		s          Stack
		inPaneTile bool
		want       SelectionOwner
	}{
		{"a grid at home", grid, false, SelectionRow},
		{"a grid inside a pane tile", grid, true, SelectionLayout},
		{"a content descent", content, false, SelectionNone},
		{"a content descent inside a pane tile", content, true, SelectionNone},
	}
	for _, c := range tests {
		if got := c.s.SelectionTarget(c.inPaneTile); got != c.want {
			t.Errorf("%s: SelectionTarget = %v, want %v", c.name, got, c.want)
		}
	}
}

// Select is sorted, empty is nil, and a clone keeps the set it held.
func TestSelectReplacesTheSetWhole(t *testing.T) {
	s := NewStack("g")
	if !s.Select([]string{"b", "a", "b"}) || !slices.Equal(s.Selection, []string{"a", "b"}) {
		t.Fatalf("Select = %v", s.Selection)
	}
	if s.Select([]string{"a", "b"}) {
		t.Error("the same set reported a change")
	}
	c := s.Clone()
	s.Select(nil)
	if s.Selection != nil || !slices.Equal(c.Selection, []string{"a", "b"}) {
		t.Errorf("after clearing: %v, clone %v", s.Selection, c.Selection)
	}
}

// Each grid frame keeps its own set in the blob, in the owning node's frame of
// ids, and comes back under the reader's chain; ascending pops the inner
// frame's set and finds the outer one's.
func TestLayoutKeepsEveryFramesSelection(t *testing.T) {
	const prefix = "ssh-a/"
	rel := func(id string) (string, bool) { return strings.CutPrefix(id, prefix) }
	abs := func(id string) string { return prefix + id }

	tr := NewTree()
	p := tr.FocusedPane()
	p.Stack = StackAt(prefix+"n/1", []string{prefix + "n/7"}, "")
	p.Select([]string{prefix + "n/9", prefix + "n/8"})
	p.Pop()
	p.Select([]string{prefix + "n/7"})
	p.Push(Frame{Door: prefix + "n/7"})
	p.Select([]string{prefix + "n/9", prefix + "n/8"})

	data, skipped, err := EncodeLayout(tr, rel)
	if err != nil || len(skipped) != 0 {
		t.Fatalf("encode: %v %v", err, skipped)
	}
	if want := `"sel":[["n/7"],["n/8","n/9"]]`; !bytes.Contains(data, []byte(want)) {
		t.Fatalf("blob %s lacks %s", data, want)
	}
	got, err := DecodeLayout(data, abs, "")
	if err != nil {
		t.Fatal(err)
	}
	gp := got.FocusedPane()
	if !slices.Equal(gp.Selection, []string{prefix + "n/8", prefix + "n/9"}) {
		t.Errorf("top selection = %v", gp.Selection)
	}
	gp.Pop()
	if !slices.Equal(gp.Selection, []string{prefix + "n/7"}) {
		t.Errorf("outer selection = %v", gp.Selection)
	}
}

// A blob with no selection encodes as it always did, and one whose sets do not
// match its place frame for frame restores none.
func TestLayoutWithoutSelectionIsUnchanged(t *testing.T) {
	tr := NewTree()
	tr.FocusedPane().Stack = StackAt("n/1", []string{"n/7"}, "")
	data, _, err := EncodeLayout(tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"sel"`)) {
		t.Errorf("an empty selection was written: %s", data)
	}
	bad := `{"v":1,"root":{"pane":{"id":"p1","anchor":"n/1","path":["n/7"],"sel":[["n/4"]]}},"focus":"p1"}`
	got, err := DecodeLayout([]byte(bad), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if s := got.FocusedPane().Selection; s != nil {
		t.Errorf("a mismatched sel restored %v", s)
	}
}
