package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/source"
	procsrc "github.com/josephburnett/gridwell/internal/source/proc"
)

// benchFSDir builds a temp dir with n files + dirSubdirs subdirectories
// and returns its path. Used to give the benchmarks a stable shape.
func benchFSDir(b *testing.B, files, dirs int) string {
	root := b.TempDir()
	for i := 0; i < files; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file_%04d", i)), []byte("x"), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < dirs; i++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("dir_%04d", i)), 0o755); err != nil {
			b.Fatal(err)
		}
	}
	return root
}

// BenchmarkFSGridFirstDescent measures the cost of the very first
// GetGrid on a fresh file-well: read the directory, synthesize one
// tile per entry, persist. Mirrors what happens when the user
// descends into a directory they haven't seen before.
func BenchmarkFSGridFirstDescent(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			dir := benchFSDir(b, n/2, n-n/2)
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				s := newBenchStore(b)
				root := mustRootID(b, s)
				w, err := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
					Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: dir,
				})
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if _, err := s.GetGrid(ctx, w.ChildGridID); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkFSGridRepeatDescent measures the cheap path: GetGrid on a
// fs-grid we've already populated. With the no-op short-circuit this
// should not require a DB write — directory read + map diff only.
// This is the hot path: every frame's cache hit potentially fires a
// fetchGrid that lands here.
func BenchmarkFSGridRepeatDescent(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			dir := benchFSDir(b, n/2, n-n/2)
			ctx := context.Background()
			s := newBenchStore(b)
			root := mustRootID(b, s)
			w, err := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
				Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: dir,
			})
			if err != nil {
				b.Fatal(err)
			}
			// Warm the grid so the first iteration measures a no-op
			// reconcile, not the first-descent cost.
			if _, err := s.GetGrid(ctx, w.ChildGridID); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.GetGrid(ctx, w.ChildGridID); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkProcGridFirstDescent measures the analogous proc-grid path,
// using a stub /proc tree so the benchmark doesn't depend on the host
// process table.
func BenchmarkProcGridFirstDescent(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("children=%d", n), func(b *testing.B) {
			// Build the stub /proc tree once outside the b.N loop.
			procRoot := b.TempDir()
			writeProcFull(b, procRoot, 1, 0, "init", "", 0)
			for i := 0; i < n; i++ {
				writeProcFull(b, procRoot, int64(2+i), 1, fmt.Sprintf("p%d", i), "", 0)
			}

			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				s := newBenchStore(b)
				reg := source.NewRegistry()
				reg.Register(procsrc.New(procRoot, nil))
				s.setRegistry(reg)
				root := mustRootID(b, s)
				w, err := s.CreateProcessWell(ctx, &rpc.CreateProcessWellRequest{
					Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, PID: 1,
				})
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if _, err := s.GetGrid(ctx, w.ChildGridID); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// newBenchStore mirrors newTestStore for benchmarks (testing.B vs T).
func newBenchStore(b *testing.B) *Store {
	b.Helper()
	s, err := Open(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	return s
}

func mustRootID(b *testing.B, s *Store) int64 {
	b.Helper()
	id, err := s.RootGridID(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	return id
}
