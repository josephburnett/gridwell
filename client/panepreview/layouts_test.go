package panepreview

import (
	"testing"

	"github.com/josephburnett/gridwell/client/pane"
)

func layoutBytes(t *testing.T) []byte {
	t.Helper()
	data, _, err := pane.EncodeLayout(pane.NewTree(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type bodyFake struct {
	data  []byte
	ok    bool
	asked int
}

func (b *bodyFake) body() ([]byte, bool) { b.asked++; return b.data, b.ok }

func TestLayoutsTree(t *testing.T) {
	var reported []string
	l := NewLayouts(func(id string, _ error) { reported = append(reported, id) })
	good := &bodyFake{data: layoutBytes(t), ok: true}

	if _, ok := l.Tree("n1/7", 0, good.body); ok || good.asked != 0 {
		t.Fatal("a never-arranged tile has no tree and asks for no bytes")
	}

	inflight := &bodyFake{}
	if _, ok := l.Tree("n1/7", 3, inflight.body); ok {
		t.Fatal("bytes in flight and nothing older: no tree")
	}
	if _, ok := l.Tree("n1/7", 3, inflight.body); ok || inflight.asked != 2 {
		t.Fatal("a miss memoizes nothing: the next frame asks again")
	}

	tree, ok := l.Tree("n1/7", 3, good.body)
	if !ok || tree == nil {
		t.Fatal("decoded bytes give a tree")
	}
	if again, ok := l.Tree("n1/7", 3, good.body); !ok || again != tree || good.asked != 1 {
		t.Fatal("the same blob answers from the memo without asking for bytes")
	}

	// A new blob whose bytes have not landed keeps the last arrangement.
	if stale, ok := l.Tree("n1/7", 4, inflight.body); !ok || stale != tree {
		t.Fatal("until new bytes land the last decoded arrangement keeps drawing")
	}

	bad := &bodyFake{data: []byte("{not a layout"), ok: true}
	if _, ok := l.Tree("n1/7", 4, bad.body); ok {
		t.Fatal("a corrupt blob has no tree")
	}
	if _, ok := l.Tree("n1/7", 4, bad.body); ok || bad.asked != 1 || len(reported) != 1 {
		t.Fatalf("a corrupt blob is memoized and reported once: asked %d, reported %v", bad.asked, reported)
	}
	if _, ok := l.Tree("n1/7", 5, good.body); !ok {
		t.Fatal("the next blob decodes afresh after a failure")
	}
}
