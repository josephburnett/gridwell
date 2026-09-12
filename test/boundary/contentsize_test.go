package boundary

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// contentSizeOwner is the file that may spell the content stream's sizes.
// Every other site names rpc.ContentChunkBytes or rpc.MaxContentBytes.
const contentSizeOwner = "api/rpc/types.go"

// The two sizes, in the spellings a second copy would plausibly be written
// in. A copy drifts in silence: a cache replaying a body in a different chunk
// shape, or a layer refusing bytes the store would have taken, is a
// difference no test of either side alone can see.
var contentSizeSpellings = regexp.MustCompile(
	`256\s*\*\s*1024|262144|1\s*<<\s*18|256\s*<<\s*10|` +
		`16\s*\*\s*1024\s*\*\s*1024|16777216|16\s*<<\s*20|1\s*<<\s*24`)

func TestContentSizesHaveOneOwner(t *testing.T) {
	root := repoRoot(t)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "gen":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || rel == contentSizeOwner || rel == filepath.Join("test", "boundary", "contentsize_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for i, line := range strings.Split(string(data), "\n") {
			if contentSizeSpellings.MatchString(line) {
				t.Errorf("%s:%d: a second spelling of a content size: %s\n"+
					"the chunk size and the body cap live in %s, as rpc.ContentChunkBytes and "+
					"rpc.MaxContentBytes. Read them from there.", rel, i+1, strings.TrimSpace(line), contentSizeOwner)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
