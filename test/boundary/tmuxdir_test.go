package boundary

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// A node started by a test writes its tmux directory (tmux.New) under
// TMPDIR, and nothing removes it, since a real node's tmuxes outlive it; a
// tmux server leaves its socket under TMUX_TMPDIR. So a test run that starts
// a node, a tmux and a shell's tmux must leave both as it found them.
func TestANodeTestLeavesNothingInTMPDIR(t *testing.T) {
	// Not t.TempDir: a node's sockets sit under it, and unix socket paths are
	// short.
	tmp, err := os.MkdirTemp("", "gw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	cmd := exec.Command("go", "test", "-count=1",
		"-run", "^(TestCloseIsIdempotent|TestCreateThenHasThenKill|TestAPasteTypesIntoARealTmux)$",
		"./internal/node/", "./internal/local/tmux/", "./internal/server/")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp, "TMUX_TMPDIR="+tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test: %v\n%s", err, out)
	}
	var left []string
	for _, pattern := range []string{"gridwell-tmux-*", "tmux-*/*"} {
		m, err := filepath.Glob(filepath.Join(tmp, pattern))
		if err != nil {
			t.Fatal(err)
		}
		left = append(left, m...)
	}
	if len(left) > 0 {
		t.Errorf("the tests left %v in TMPDIR or TMUX_TMPDIR", left)
	}
}

// Every test binary that can start a node, by linking the tmux package or
// by spawning `gridwell serve`, runs under tmuxtest.Main, so a test added to
// it cannot leave a tmux directory behind.
func TestEveryNodeTestRedirectsTMPDIR(t *testing.T) {
	root := repoRoot(t)
	cmd := exec.Command("go", "list", "-test", "-tags", "connections", "-f", "{{.ImportPath}}|{{.Dir}}|{{join .Deps \" \"}}", "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok {
		t.Fatalf("go list: %v\n%s", err, ee.Stderr)
	} else if err != nil {
		t.Fatal(err)
	}
	spawn := regexp.MustCompile(`exec\.Command\([^\n]*"serve"`)
	checked := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 || !strings.HasSuffix(parts[0], ".test") {
			continue
		}
		tests, err := filepath.Glob(filepath.Join(parts[1], "*_test.go"))
		if err != nil {
			t.Fatal(err)
		}
		var src strings.Builder
		for _, f := range tests {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			src.Write(b)
		}
		links := slices.Contains(strings.Fields(parts[2]), repoModule+"/internal/local/tmux")
		if !links && !spawn.MatchString(src.String()) {
			continue
		}
		checked++
		if !strings.Contains(src.String(), "tmuxtest.Main(m)") {
			t.Errorf("%s can start a node and has no TestMain calling tmuxtest.Main", strings.TrimSuffix(parts[0], ".test"))
		}
	}
	if checked == 0 {
		t.Fatal("no test binary can start a node; the listing is wrong")
	}
}
