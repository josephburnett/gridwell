package textedit

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cadence"
	"github.com/josephburnett/gridwell/client/debounce"
	"github.com/josephburnett/gridwell/client/pane"
)

// settleClock is a debounce Schedule and Clock on virtual time.
type settleClock struct {
	now  float64
	dues []float64
	runs []func()
}

func (c *settleClock) schedule(ms int, fire func()) {
	c.dues = append(c.dues, c.now+float64(ms))
	c.runs = append(c.runs, fire)
}

func (c *settleClock) advance(ms float64) {
	c.now += ms
	for i := 0; i < len(c.runs); i++ {
		if c.dues[i] <= c.now {
			run := c.runs[i]
			c.dues = append(c.dues[:i], c.dues[i+1:]...)
			c.runs = append(c.runs[:i], c.runs[i+1:]...)
			run()
			i = -1
		}
	}
}

// A scroll is a framing change like a pan. The shim mirrors it onto the pane
// through pane.Frame.ScrollText and redraws when it moved; the redraw arms the
// settle persister on pane.FramingFingerprint, and the flush asks Reframes of
// every pane's row. This is that chain without the shim: a burst of scrolls
// writes once, after the settle, and a row nobody scrolled writes nothing.
func TestAScrollPersistsOnceAndStampsNoOtherRow(t *testing.T) {
	tree := pane.NewTree()
	a := tree.FocusedPane()
	a.Push(pane.ContentFrame("doc-a", pane.Footprint{W: 1, H: 1}, 1, rpc.TextModeText, 0, 0))
	b, err := tree.Split(pane.Vertical)
	if err != nil {
		t.Fatal(err)
	}
	b.Pop()
	b.Push(pane.ContentFrame("doc-b", pane.Footprint{X: 2, W: 1, H: 1}, 1, rpc.TextModeText, 0, 0))

	box := Box{W: 600, H: 400}
	rows := map[string]Framing{"doc-a": {}, "doc-b": {}} // both never framed
	var writes []string
	flush := func() {
		tree.Walk(func(p *pane.Pane) {
			next := Framing{X: int64(p.TextScrollX + 0.5), Y: int64(p.TextScrollY + 0.5),
				W: box.W, H: box.H, Mode: p.TextMode}
			if Reframes(rows[p.ContentID()], next, false) {
				rows[p.ContentID()] = next
				writes = append(writes, p.ContentID())
			}
		})
	}
	clock := &settleClock{}
	persister := debounce.New(clock.schedule, func() float64 { return clock.now },
		cadence.FramingSaveMode, flush)
	draw := func() {
		persister.ArmOnChange(cadence.FramingSaveMs, pane.FramingFingerprint(tree).Value())
	}
	scroll := func(p *pane.Pane, y float64) {
		if p.ScrollText(p.TextScrollX, y) {
			draw()
		}
	}

	draw() // the descents
	clock.advance(cadence.FramingSaveMs)
	if len(writes) != 0 {
		t.Fatalf("looking at two documents wrote %v", writes)
	}

	for _, y := range []float64{120, 240, 400} {
		scroll(a, y)
		clock.advance(cadence.FramingSaveMs / 4)
	}
	if len(writes) != 0 {
		t.Fatalf("the scroll wrote %v before it settled", writes)
	}
	clock.advance(cadence.FramingSaveMs)
	if len(writes) != 1 || writes[0] != "doc-a" || rows["doc-a"].Y != 400 {
		t.Fatalf("a settled scroll wrote %v with doc-a at %+v, want one write of y 400", writes, rows["doc-a"])
	}

	// A scroll event that lands where the pane already is, as the restore of a
	// stored scroll fires one, moves nothing and arms nothing.
	if a.ScrollText(a.TextScrollX, 400) {
		t.Error("a scroll to where the pane already is reported a move")
	}
	draw()
	clock.advance(cadence.FramingSaveMs * 2)
	if len(writes) != 1 {
		t.Errorf("a redraw after the settle wrote again: %v", writes)
	}
	if rows["doc-b"] != (Framing{}) {
		t.Errorf("the document nobody scrolled was stamped %+v", rows["doc-b"])
	}
}
