package boundary

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// commentShareCeiling is the most of the production tree, by bytes, that may
// be comments. The 2026-09-07 sweep brought it from 54% to the number below;
// a reader holds a package in one sitting, and comments restated at every
// call site are what push it past that.
const commentShareCeiling = 40

// TestCommentShareStaysUnderCeiling runs scripts/comment-share.sh, the one
// owner of the measure, and fails when the tree's comment share exceeds
// commentShareCeiling.
func TestCommentShareStaysUnderCeiling(t *testing.T) {
	root := repoRoot(t)
	out, err := exec.Command("sh", filepath.Join(root, "scripts", "comment-share.sh")).Output()
	if err != nil {
		t.Fatalf("comment-share.sh: %v", err)
	}
	m := regexp.MustCompile(`comment share (\d+)%`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("comment-share.sh printed no share: %q", out)
	}
	share, _ := strconv.Atoi(string(m[1]))
	if share > commentShareCeiling {
		t.Errorf("comment share is %d%%, over the %d%% ceiling: a comment says what the code cannot, once, at the owner (CLAUDE.md, Comments)", share, commentShareCeiling)
	}
}
