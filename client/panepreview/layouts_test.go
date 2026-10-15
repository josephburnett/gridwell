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

	if _, ok := l.Tree("n1/7", 0, 1, good.body); ok || good.asked != 0 {
		t.Fatal("a never-arranged tile has no tree and asks for no bytes")
	}

	inflight := &bodyFake{}
	if _, ok := l.Tree("n1/7", 3, 0, inflight.body); ok {
		t.Fatal("bytes in flight and nothing older: no tree")
	}
	if _, ok := l.Tree("n1/7", 3, 0, inflight.body); ok || inflight.asked != 2 {
		t.Fatal("a miss memoizes nothing: the next frame asks again")
	}

	tree, ok := l.Tree("n1/7", 3, 1, good.body)
	if !ok || tree == nil {
		t.Fatal("decoded bytes give a tree")
	}
	if again, ok := l.Tree("n1/7", 3, 1, good.body); !ok || again != tree || good.asked != 1 {
		t.Fatal("the same bytes answer from the memo without asking for them")
	}
	if again, ok := l.Tree("n1/7", 4, 1, good.body); !ok || again != tree || good.asked != 1 {
		t.Fatal("the memo is keyed by the bytes, not the row's blob")
	}

	// Once the cache holds no bytes there is no arrangement: the last one may
	// name tiles the row no longer does.
	if _, ok := l.Tree("n1/7", 4, 0, inflight.body); ok {
		t.Fatal("dropped bytes' arrangement still answered")
	}

	bad := &bodyFake{data: []byte("{not a layout"), ok: true}
	if _, ok := l.Tree("n1/7", 4, 2, bad.body); ok {
		t.Fatal("a corrupt body has no tree")
	}
	if _, ok := l.Tree("n1/7", 4, 2, bad.body); ok || bad.asked != 1 || len(reported) != 1 {
		t.Fatalf("a corrupt body is memoized and reported once: asked %d, reported %v", bad.asked, reported)
	}
	if _, ok := l.Tree("n1/7", 5, 3, good.body); !ok {
		t.Fatal("the next bytes decode afresh after a failure")
	}
}
