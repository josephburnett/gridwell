package boundary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FailedPrecondition is a stale claim to every reader (gwerr.IsConflict), so
// it is named only in api/gwerr, which owns the conflict class, its detailed
// cousins and the predicates, and by the producers below, each a conflict or
// the PTY owner's tmux gone (gwerr.IsTmuxGone). A reader asks a predicate,
// never the code; a new refusal answers another code, as a connection's
// landing mismatch answers InvalidArgument. The two DisableSource refusals
// are counted until they move to another code.
func TestFailedPreconditionHasNamedProducersAndNoBareReaders(t *testing.T) {
	root := repoRoot(t)
	allowed := map[string]int{
		"internal/files/content.go": 1, // a stale content stamp
		"internal/local/local.go":   1, // a started tmux that is gone
		"internal/server/router.go": 2, // DisableSource's refusals
		"internal/server/reach.go":  1, // the comment saying why it answers InvalidArgument
	}
	needle := "Failed" + "Precondition"
	got := map[string]int{}
	for _, top := range []string{"api", "client", "internal", "cmd"} {
		_ = filepath.WalkDir(filepath.Join(root, top), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "api/gwerr/") || strings.HasPrefix(rel, "api/gen/") {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if n := strings.Count(string(data), needle); n > 0 {
				got[rel] = n
			}
			return nil
		})
	}
	for f, n := range got {
		if n > allowed[f] {
			t.Errorf("%s names %s %d times, %d allowed: produce a gwerr constructor's error and read it with gwerr.IsConflict or IsTmuxGone", f, needle, n, allowed[f])
		}
	}
	for f, n := range allowed {
		if got[f] < n {
			t.Errorf("%s names %s %d times, %d recorded: lower its count here", f, needle, got[f], n)
		}
	}
}
