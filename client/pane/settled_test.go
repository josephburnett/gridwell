package pane

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

func paneSize(t *testing.T, w, h float64) zoomtrans.Size {
	t.Helper()
	s, ok := zoomtrans.SizeOf(w, h)
	if !ok {
		t.Fatalf("size %vx%v", w, h)
	}
	return s
}

var settledDoor = zoomtrans.Door{ID: "9", W: 4, H: 3}

// The framing is decided when the user frames it: a split shows the same view
// in two smaller panes, and neither writes until one of them moves.
func TestSplitWritesNoFramingUntilAPaneMoves(t *testing.T) {
	tr := NewTree()
	p := tr.FocusedPane()
	p.Stack = NewStack("home/1")
	p.Push(Frame{Door: "9"})
	// The descent lands on the door's stored view at the full pane.
	full := paneSize(t, 1200, 800)
	landed, _ := zoomtrans.StoredView(settledDoor, full, 64)
	p.SetView(landed.Cx(), landed.Cy(), landed.Zoom())
	shown := zoomtrans.ShownDoorFraming(settledDoor)
	if f, ok := p.FramingWrite(shown, settledDoor, full, 64); ok {
		t.Fatalf("the landing wrote %v", f)
	}

	sib, err := tr.Split(Vertical)
	if err != nil {
		t.Fatal(err)
	}
	half := paneSize(t, 600, 800)
	for _, q := range []*Pane{p, sib} {
		if f, ok := q.FramingWrite(shown, settledDoor, half, 64); ok {
			t.Fatalf("pane %s wrote %v after a split it did not move in", q.ID, f)
		}
	}

	// The next pan in the new pane writes, as the ratio at its own size.
	sib.Steer(landed.Cx()+1, landed.Cy(), landed.Zoom())
	f, ok := sib.FramingWrite(shown, settledDoor, half, 64)
	if !ok {
		t.Fatal("a pan after the split wrote nothing")
	}
	want := zoomtrans.IntrinsicFromLive(landed.Zoom(), zoomtrans.OvertakeZoom(settledDoor, half, 64))
	if f.Cx() != landed.Cx()+1 || f.Zoom() != want {
		t.Fatalf("wrote %v, want cx %v zoom %v", f, landed.Cx()+1, want)
	}
	sib.Settle()
	if _, ok := sib.FramingWrite(rpc.Saved(f), settledDoor, half, 64); ok {
		t.Fatal("a written view wrote again")
	}
	if _, ok := p.FramingWrite(shown, settledDoor, half, 64); ok {
		t.Fatal("the pane that did not move wrote")
	}
}

// A resize alone is not framing: a pane keeps showing its pixels and writes
// nothing, and a grid only looked at is never given a framing.
func TestResizeAloneWritesNoFraming(t *testing.T) {
	s := NewStack("home/1")
	s.Push(Frame{Door: "9"})
	s.SetView(2, 1.5, 0.9)
	shown := zoomtrans.ShownDoorFraming(settledDoor)
	for _, sz := range []zoomtrans.Size{paneSize(t, 1200, 800), paneSize(t, 300, 900), paneSize(t, 50, 50)} {
		if f, ok := s.FramingWrite(shown, settledDoor, sz, 64); ok {
			t.Fatalf("an unmoved view wrote %v at %v", f, sz)
		}
	}
	// A view steered back where it settled is where the writer saw it.
	s.Steer(3, 1.5, 0.9)
	s.Steer(2, 1.5, 0.9)
	if f, ok := s.FramingWrite(shown, settledDoor, paneSize(t, 300, 900), 64); ok {
		t.Fatalf("a view steered back to where it settled wrote %v", f)
	}
}
