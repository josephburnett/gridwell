package source

import (
	"sort"
	"testing"
)

func node(key, label string) Node { return Node{Key: key, Label: label} }

func keys(ns []Node) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Key
	}
	sort.Strings(out)
	return out
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
	}
}

func TestReconcileInsertsNewKeys(t *testing.T) {
	existing := []ExistingTile{{Key: "a", Label: "a"}}
	listing := Listing{Authoritative: true, Nodes: []Node{node("a", "a"), node("b", "b")}}
	plan := Reconcile(existing, listing, nil)
	eq(t, "insert", keys(plan.Insert), []string{"b"})
	if len(plan.Delete) != 0 || len(plan.Relabel) != 0 {
		t.Errorf("unexpected delete/relabel: %+v", plan)
	}
}

func TestReconcileUnchangedTileUntouched(t *testing.T) {
	// The core rule: a key present with the same label produces NO plan
	// entry — the tile stays exactly as it was (id, placement preserved).
	existing := []ExistingTile{{Key: "a", Label: "a"}}
	listing := Listing{Authoritative: true, Nodes: []Node{node("a", "a")}}
	plan := Reconcile(existing, listing, nil)
	if len(plan.Insert)+len(plan.Relabel)+len(plan.Delete) != 0 {
		t.Errorf("unchanged tile should yield empty plan, got %+v", plan)
	}
}

func TestReconcileRelabelInPlace(t *testing.T) {
	existing := []ExistingTile{{Key: "200", Label: "bash"}}
	listing := Listing{Authoritative: false, Nodes: []Node{node("200", "zsh")}}
	plan := Reconcile(existing, listing, func(string) Presence { return PresenceGone })
	if len(plan.Relabel) != 1 || plan.Relabel[0].Key != "200" || plan.Relabel[0].Label != "zsh" {
		t.Fatalf("relabel = %+v", plan.Relabel)
	}
	if len(plan.Insert) != 0 || len(plan.Delete) != 0 {
		t.Errorf("relabel should not insert/delete: %+v", plan)
	}
}

func TestReconcileAuthoritativeSweepsMissing(t *testing.T) {
	// fs semantics: a key absent from an authoritative listing is gone.
	existing := []ExistingTile{{Key: "a", Label: "a"}, {Key: "b", Label: "b"}}
	listing := Listing{Authoritative: true, Nodes: []Node{node("a", "a")}}
	plan := Reconcile(existing, listing, nil)
	eq(t, "delete", plan.Delete, []string{"b"})
}

func TestReconcileNonAuthoritativeSweepsOnlyGone(t *testing.T) {
	// proc semantics: a missing key is swept only when probe says GONE; a
	// transient/unknown read keeps the tile (and its id/placement).
	existing := []ExistingTile{
		{Key: "gone", Label: "x"},
		{Key: "unreadable", Label: "y"},
	}
	listing := Listing{Authoritative: false, Nodes: nil}
	probe := func(key string) Presence {
		if key == "gone" {
			return PresenceGone
		}
		return PresenceUnknown // a failed read must not delete
	}
	plan := Reconcile(existing, listing, probe)
	eq(t, "delete", plan.Delete, []string{"gone"})
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("fs"); ok {
		t.Error("empty registry should miss")
	}
	r.Register(fakeSource{kind: "fs"})
	r.Register(fakeSource{kind: "proc"})
	if _, ok := r.Get("fs"); !ok {
		t.Error("fs should resolve")
	}
	if len(r.Kinds()) != 2 {
		t.Errorf("kinds = %v", r.Kinds())
	}
}

// fakeSource is a no-op Source used only to exercise the registry.
type fakeSource struct {
	Source
	kind string
}

func (f fakeSource) Info() Descriptor { return Descriptor{Kind: f.kind} }
