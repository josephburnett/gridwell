package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugintest/gitlabfake"
)

// The state directory across a restart, through the whole shipped stack: fake
// GitLab → the spawned gridwell-plugin-gitlab binary → adapter.
//
// The node hands the plugin a private directory as state_dir and never empties
// it. The gitlab plugin keeps its walked todo list there, so the process that
// comes back answers every listing from the file — no GitLab request at all
// while the last walk is still inside the refresh window. That is the whole
// point of the directory, and it is only true at the seam: the plugin's unit
// tests can prove the file round-trips, but only a spawn proves the node hands
// the key over, that the value is a directory the plugin may write, and that
// the file survives the process that wrote it.
//
// The restart runs over a FRESH node store, so the node remembers nothing of
// its own: every entry below can only have come out of the plugin's file.
func TestGitLabPluginServesFromItsStateDirAfterARestart(t *testing.T) {
	done := gitlabTodo(3, "2026-08-19T10:00:00Z")
	done.State = "done"
	gl := gitlabfake.New(t,
		gitlabTodo(1, "2026-08-18T10:00:00Z"),
		gitlabTodo(2, "2026-08-25T10:00:00Z"),
		done,
	)
	// The state directory is the test's, not the harness's per-spawn one, so
	// both processes share it — the node's own directory is per plugin id and
	// outlives any restart the same way.
	stateDir := t.TempDir()
	// A refresh window far longer than the test: the restart lands inside it,
	// so the second process has no reason to walk. If it answers, it answered
	// from the file.
	cfg := gl.Config(t, map[string]string{"refresh": "1h", "state_dir": stateDir})
	ctx := context.Background()

	client, _, closeStack := gitlabStackAt(t, filepath.Join(t.TempDir(), "mem.db"), cfg)
	info, err := client.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	week := tileByLabelPrefix(root.Tiles, "2026-08-17")
	if len(root.Tiles) != 2 || week == nil {
		t.Fatalf("root = %v", root.Tiles)
	}
	if _, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: week.ChildGridId}); err != nil {
		t.Fatal(err)
	}
	walked := gl.Calls()
	if walked == 0 {
		t.Fatal("the first process never walked GitLab")
	}
	closeStack()

	entries, err := os.ReadDir(stateDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("the plugin wrote nothing into its state_dir: %v (%v)", entries, err)
	}

	client2, _, _ := gitlabStackAt(t, filepath.Join(t.TempDir(), "mem2.db"), cfg)
	info2, err := client2.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	root2, err := client2.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info2.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	week2 := tileByLabelPrefix(root2.Tiles, "2026-08-17")
	if len(root2.Tiles) != 2 || week2 == nil {
		t.Fatalf("root after the restart = %v", root2.Tiles)
	}
	wk2, err := client2.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: week2.ChildGridId})
	if err != nil {
		t.Fatal(err)
	}
	one := tileByLabelPrefix(wk2.Tiles, "Ada: !1 ")
	three := tileByLabelPrefix(wk2.Tiles, "✅ Ada: !3 ")
	if len(wk2.Tiles) != 2 || one == nil || three == nil {
		t.Fatalf("week after the restart = %v", wk2.Tiles)
	}
	// The todo's body reads from the file too, not from a walk.
	if body := readContent(t, client2, one.Id); !strings.Contains(body, "[Open !1 in GitLab](") {
		t.Errorf("content after the restart = %q", body)
	}
	if got := gl.Calls(); got != walked {
		t.Errorf("the restart made %d GitLab requests: it re-walked instead of reading its state_dir", got-walked)
	}
}

// A plugin handed no state_dir — an older node, a hand-launched binary — must
// still serve. It then keeps its memory for its process lifetime and walks
// GitLab at every start, which is what it always did.
func TestGitLabPluginWithoutAStateDirStillServes(t *testing.T) {
	gl := gitlabfake.New(t, gitlabTodo(1, "2026-08-18T10:00:00Z"))
	// A blank value is how a config carries "no directory". The harness mints
	// one for a spawn that names none, so this test names a blank one; the
	// plugin trims it, exactly as it trims every other config value.
	cfg := gl.Config(t, map[string]string{"refresh": "1h", "state_dir": " "})
	client, _, _ := gitlabStackAt(t, filepath.Join(t.TempDir(), "mem.db"), cfg)
	ctx := context.Background()
	info, err := client.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Tiles) != 1 || gl.Calls() == 0 {
		t.Fatalf("root = %v after %d GitLab requests", root.Tiles, gl.Calls())
	}
}
