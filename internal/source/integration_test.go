package source_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/josephburnett/gridwell/internal/source"
	fssrc "github.com/josephburnett/gridwell/internal/source/fs"
	procsrc "github.com/josephburnett/gridwell/internal/source/proc"
)

// existingFrom turns a Listing into the minimal tile view the store would
// already hold after a prior reconcile.
func existingFrom(l source.Listing) []source.ExistingTile {
	out := make([]source.ExistingTile, len(l.Nodes))
	for i, n := range l.Nodes {
		out[i] = source.ExistingTile{Key: n.Key, Label: n.Label}
	}
	return out
}

func has(keys []string, want string) bool { return slices.Contains(keys, want) }

func relabelKeys(rs []source.Relabel) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Key
	}
	return out
}

func insertKeys(ns []source.Node) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Key
	}
	return out
}

// TestFSReconcileAcrossChanges drives the fs source + Reconcile over two
// passes: add a file, remove a file. The unchanged entries must not appear
// in the plan at all (their tiles keep id + placement).
func TestFSReconcileAcrossChanges(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt")
	write("b.txt")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := fssrc.New(nil)
	ctx := context.Background()

	first, err := s.List(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	existing := existingFrom(first)

	// Mutate the directory underneath us.
	write("c.txt")
	if err := os.Remove(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}

	second, err := s.List(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	plan := source.Reconcile(existing, second, func(key string) source.Presence {
		p, _ := s.Probe(ctx, dir, key)
		return p
	})

	if ins := insertKeys(plan.Insert); !has(ins, "c.txt") || len(ins) != 1 {
		t.Errorf("insert = %v, want [c.txt]", ins)
	}
	if !has(plan.Delete, "b.txt") || len(plan.Delete) != 1 {
		t.Errorf("delete = %v, want [b.txt]", plan.Delete)
	}
	// a.txt and sub were unchanged → they must be in no plan list.
	for _, k := range []string{"a.txt", "sub"} {
		if has(insertKeys(plan.Insert), k) || has(plan.Delete, k) || has(relabelKeys(plan.Relabel), k) {
			t.Errorf("unchanged %q leaked into plan %+v", k, plan)
		}
	}
}

// TestProcReconcileSweepsOnlyExited drives the proc source + Reconcile: a
// child that exited (its /proc dir gone) is swept via the non-authoritative
// probe; the surviving child and @info are untouched.
func TestProcReconcileSweepsOnlyExited(t *testing.T) {
	root := t.TempDir()
	writeProc := func(pid, ppid int64, name string) {
		dir := filepath.Join(root, strconv.FormatInt(pid, 10))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		status := fmt.Sprintf("Name:\t%s\nState:\tS (sleeping)\nPPid:\t%d\nUid:\t1000\t1000\t1000\t1000\nThreads:\t1\n", name, ppid)
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "cmdline"), []byte(name), 0o644)
	}
	writeProc(100, 1, "parent")
	writeProc(200, 100, "childA")
	writeProc(201, 100, "childB")

	s := procsrc.New(root, nil)
	ctx := context.Background()

	first, err := s.List(ctx, "100")
	if err != nil {
		t.Fatal(err)
	}
	existing := existingFrom(first)

	// Child 201 exits — its /proc entry vanishes.
	if err := os.RemoveAll(filepath.Join(root, "201")); err != nil {
		t.Fatal(err)
	}

	second, err := s.List(ctx, "100")
	if err != nil {
		t.Fatal(err)
	}
	plan := source.Reconcile(existing, second, func(key string) source.Presence {
		p, _ := s.Probe(ctx, "100", key)
		return p
	})

	if !has(plan.Delete, "201") || len(plan.Delete) != 1 {
		t.Errorf("delete = %v, want [201]", plan.Delete)
	}
	for _, k := range []string{"200", "@info"} {
		if has(plan.Delete, k) {
			t.Errorf("surviving %q was swept; plan %+v", k, plan)
		}
	}
}
