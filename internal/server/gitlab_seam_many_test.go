package server

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/internal/plugintest/gitlabfake"
)

// The spawned plugin's real HTTP client against a PAGINATED fake GitLab
// (X-Next-Page, 100 per page, newest first — the shape the first cut was
// never exercised against): 260 todos over 14 weeks, 20 of
// the newest pending. Pins: the outset walk is one pass per state plus
// the pages it takes; every week gets its own counts; the root is a
// calendar (a row per month, weeks left to right); a week descent is
// answered from memory; and each todo tile reads as markdown.
func TestGitLabManyWeeksThroughPaginatedAPI(t *testing.T) {
	// The todos' own created_at spreads them over the weeks; the clock is
	// nobody's business but the refresh window's, which the default leaves
	// wide, so the descent below is answered from memory.
	base := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	var all []gitlabfake.Todo
	for i := 0; i < 260; i++ {
		var td gitlabfake.Todo
		td.ID, td.State = int64(1000-i), "done"
		if i < 20 {
			td.State = "pending"
		}
		td.CreatedAt = base.Add(-time.Duration(i) * 8 * time.Hour).Format("2006-01-02T15:04:05.000Z")
		td.ActionName, td.TargetType = "assigned", "MergeRequest"
		td.Target.IID, td.Target.Title, td.Target.State = int64(500-i), fmt.Sprintf("MR %d", i), "opened"
		td.Project.ID, td.Project.Name, td.Project.PathWithNamespace = 1, "p", "g/p"
		td.Author.Name, td.Author.Username = "A", "a"
		td.TargetURL, td.Body = "https://gl/x", "body"
		all = append(all, td)
	}
	gl := gitlabfake.New(t, all...)

	memPath := filepath.Join(t.TempDir(), "mem.db")
	client, cp, closeStack := gitlabStackAt(t, memPath, gl.Config(t, nil))
	defer closeStack()
	ctx := context.Background()
	info, err := client.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	// 20 pending on one page + 240 done on three pages.
	if len(root.Tiles) != 14 || gl.Calls() != 4 {
		t.Fatalf("root = %d weeks after %d calls, want 14 weeks after 4", len(root.Tiles), gl.Calls())
	}
	// The counts are the plugin's own summary of the walk. They are read at
	// the plugin door, where they live: Grid.source_id, the node field that
	// used to carry the label onward, was retired with source_kind — nothing
	// on the node surface or the client ever read it. Without this the
	// pagination arithmetic would have no assertion at all.
	pinfo, err := cp.Info(ctx, &pluginv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	lst, err := cp.List(ctx, &pluginv1.ListRequest{Context: pinfo.GetRootContext()})
	if err != nil {
		t.Fatal(err)
	}
	if lst.GetSourceLabel() != "gitlab todos · 20 open · 240 done" {
		t.Errorf("root source label = %q", lst.GetSourceLabel())
	}
	labels := map[string]bool{}
	rows := map[int64]int{}
	for _, tl := range root.Tiles {
		labels[tl.AltText] = true
		rows[tl.Y]++
		if tl.X < 0 || tl.X > 4 {
			t.Errorf("week %s at x=%d: a month has at most five Mondays", tl.AltText, tl.X)
		}
	}
	if len(labels) < 4 {
		t.Errorf("weeks share labels: %v", labels)
	}
	// August (row 0) and July (row 1) hold four or five weeks each.
	if rows[0] < 4 || rows[1] < 4 || rows[2] < 4 {
		t.Errorf("month rows = %v, want a calendar", rows)
	}
	var thisWeek *gridwellv1.Tile
	for _, tl := range root.Tiles {
		if tl.AltText == "2026-08-24 · 5 open · 0 done" {
			thisWeek = tl
		}
	}
	if thisWeek == nil || thisWeek.X != 3 || thisWeek.Y != 0 {
		t.Fatalf("this week = %+v, want the fourth Monday of the epoch month at (3,0)", thisWeek)
	}

	// Descent: answered from memory (no new calls), every tile a page
	// with a face.
	wk, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: thisWeek.ChildGridId})
	if err != nil {
		t.Fatal(err)
	}
	if len(wk.Tiles) != 5 || gl.Calls() != 4 {
		t.Fatalf("week = %d tiles after %d calls", len(wk.Tiles), gl.Calls())
	}
	for _, tl := range wk.Tiles {
		if tl.Kind != "text" || tl.ServesPage {
			t.Errorf("tile %s: kind=%s serves_page=%v, want a markdown text tile", tl.AltText, tl.Kind, tl.ServesPage)
		}
	}
	if body := readContent(t, client, wk.Tiles[0].Id); !strings.Contains(body, "[Open !") {
		t.Fatalf("content = %q", body)
	}
}
