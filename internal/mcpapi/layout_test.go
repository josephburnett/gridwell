package mcpapi

import (
	"slices"
	"strings"
	"testing"
)

// A workspace tile's layout answers its panes in tree order, ids spelled as the
// tile's own: a workspace tile behind a connection gets the connection's prefix.
// A grid is named only where the blob names it.
func TestLayoutOfAnswersThePanesUnderTheTilesChain(t *testing.T) {
	blob := `{"v":1,"root":{"split":{"dir":"v","ratio":0.5,"a":{"pane":{"id":"p1","anchor":"far/1"}},` +
		`"b":{"pane":{"id":"p2","anchor":"far/1","path":["far/7"],"text_focus":"far/9","views":[{"x":1,"y":1,"z":1}]}}}},"focus":"p2"}`
	l, err := layoutOf("me/laptop/far/5", []byte(blob), 4)
	if err != nil {
		t.Fatal(err)
	}
	if l.Version != 4 || len(l.Panes) != 2 {
		t.Fatalf("layout = %+v, want two panes at version 4", l)
	}
	if p := l.Panes[0]; p.ID != "p1" || p.Anchor != "me/laptop/far/1" || p.Grid != p.Anchor || p.Focused {
		t.Errorf("pane 1 = %+v, want unfocused on me/laptop/far/1", p)
	}
	if p := l.Panes[1]; p.ID != "p2" || !slices.Equal(p.Path, []string{"me/laptop/far/7"}) || p.Grid != "" ||
		p.Content != "me/laptop/far/9" || !p.Focused {
		t.Errorf("pane 2 = %+v, want focused inside me/laptop/far/9 through me/laptop/far/7", p)
	}
	if _, err := layoutOf("me/5", []byte(`{"v":99}`), 1); err == nil || !strings.HasPrefix(err.Error(), "refused:") {
		t.Errorf("a newer blob = %v, want refused", err)
	}
}
