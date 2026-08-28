package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/plugins/gitlab/gitlabapi"
	gitlabplugin "github.com/josephburnett/gridwell/plugins/gitlab/plugin"
)

// The real HTTP client against a PAGINATED fake GitLab (X-Next-Page, 100
// per page, newest first — the shape the first cut was never exercised
// against; Joe's laptop, 2026-08-27): 260 todos over 14 weeks, 20 of
// the newest pending. Pins: the outset walk is one pass per state plus
// the pages it takes; every week gets its own counts; the root is a
// calendar (a row per month, weeks left to right); a week descent is
// answered from memory; and each todo tile reads as markdown.
func TestGitLabManyWeeksThroughPaginatedAPI(t *testing.T) {
	base := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	type todo struct {
		ID         int64          `json:"id"`
		State      string         `json:"state"`
		CreatedAt  string         `json:"created_at"`
		ActionName string         `json:"action_name"`
		TargetType string         `json:"target_type"`
		Target     map[string]any `json:"target"`
		Project    map[string]any `json:"project"`
		Author     map[string]any `json:"author"`
		TargetURL  string         `json:"target_url"`
		Body       string         `json:"body"`
	}
	var all []todo
	for i := 0; i < 260; i++ {
		state := "done"
		if i < 20 {
			state = "pending"
		}
		all = append(all, todo{
			ID: int64(1000 - i), State: state,
			CreatedAt:  base.Add(-time.Duration(i) * 8 * time.Hour).Format("2006-01-02T15:04:05.000Z"),
			ActionName: "assigned", TargetType: "MergeRequest",
			Target:    map[string]any{"iid": 500 - i, "title": fmt.Sprintf("MR %d", i), "state": "opened"},
			Project:   map[string]any{"id": 1, "name": "p", "path_with_namespace": "g/p"},
			Author:    map[string]any{"name": "A", "username": "a"},
			TargetURL: "https://gl/x", Body: "body",
		})
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		state := r.URL.Query().Get("state")
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		var sel []todo
		for _, td := range all {
			if td.State == state {
				sel = append(sel, td)
			}
		}
		start := min((page-1)*per, len(sel))
		end := min(start+per, len(sel))
		if end < len(sel) {
			w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sel[start:end])
	}))
	defer srv.Close()

	memPath := filepath.Join(t.TempDir(), "mem.db")
	client, closeStack := gitlabStackAt(t, memPath, gitlabplugin.New(gitlabapi.New(srv.URL, "tok", nil), gitlabplugin.Options{Now: func() time.Time { return base }}))
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
	if len(root.Tiles) != 14 || calls != 4 {
		t.Fatalf("root = %d weeks after %d calls, want 14 weeks after 4", len(root.Tiles), calls)
	}
	if root.Grid.SourceId != "gitlab todos · 20 open · 240 done" {
		t.Errorf("root source label = %q", root.Grid.SourceId)
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
	if len(wk.Tiles) != 5 || calls != 4 {
		t.Fatalf("week = %d tiles after %d calls", len(wk.Tiles), calls)
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
