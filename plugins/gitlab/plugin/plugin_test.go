package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/plugins/gitlab/todos"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func mk(id int64, created, state string) todos.Todo {
	var t todos.Todo
	t.ID, t.CreatedAt, t.State = id, at(created), state
	t.TargetType, t.Target.IID, t.Target.Title, t.Body = "MergeRequest", id, "mr "+strings.Repeat("x", int(id)), "please **review**"
	t.TargetURL = "https://gitlab.example/g/p/-/merge_requests/1"
	return t
}

// oneShot serves whole lists in one page and counts calls.
type oneShot struct {
	pending, done []todos.Todo
	calls         int
}

func (f *oneShot) Page(_ context.Context, state string, page int) ([]todos.Todo, bool, error) {
	f.calls++
	if page > 1 {
		return nil, false, nil
	}
	if state == todos.StateDone {
		return f.done, false, nil
	}
	return f.pending, false, nil
}

type sink struct {
	pluginv1.Plugin_ServeContentServer
	chunks []*pluginv1.ServeContentChunk
}

func (s *sink) Send(c *pluginv1.ServeContentChunk) error { s.chunks = append(s.chunks, c); return nil }
func (s *sink) Context() context.Context                 { return context.Background() }

func TestListsWeeksThenTodosAndRefreshesOnAWindow(t *testing.T) {
	src := &oneShot{
		pending: []todos.Todo{mk(1, "2026-08-18T10:00:00Z", "pending"), mk(2, "2026-08-25T10:00:00Z", "pending")},
		done:    []todos.Todo{mk(3, "2026-08-19T10:00:00Z", "done")},
	}
	clock := at("2026-08-25T12:00:00Z")
	p := New(src, nil, Options{Now: func() time.Time { return clock }})
	ctx := context.Background()

	info, _ := p.Info(ctx, &pluginv1.InfoRequest{})
	if info.Kind != Kind || info.RootContext != todos.RootContext {
		t.Fatalf("info = %v", info)
	}
	root, err := p.List(ctx, &pluginv1.ListRequest{Context: todos.RootContext})
	if err != nil {
		t.Fatal(err)
	}
	if root.Authoritative || len(root.Entries) != 2 || root.Entries[0].Key != "week:2026-08-24" || root.Entries[1].Label != "2026-08-17 · 1 open · 1 done" {
		t.Fatalf("root = %v", root.Entries)
	}
	if src.calls != 2 {
		t.Fatalf("outset walk made %d calls, want 2 (pending + done)", src.calls)
	}
	// The root walk covered every week: a descent inside the window is
	// answered from memory, no GitLab round trip.
	wk, err := p.List(ctx, &pluginv1.ListRequest{Context: "week:2026-08-17"})
	if err != nil {
		t.Fatal(err)
	}
	if src.calls != 2 {
		t.Errorf("a fresh week re-walked GitLab (%d calls)", src.calls)
	}
	if len(wk.Entries) != 2 || !wk.Entries[0].ServesPage || wk.Entries[0].Kind != "text" || wk.Entries[0].Key != "todo:1" {
		t.Fatalf("week = %v", wk.Entries)
	}
	if h := wk.Entries[0].PlacementHint; h == nil || h.X != todos.TodoTileW || h.W != todos.TodoTileW {
		t.Errorf("Tuesday hint = %v", wk.Entries[0].PlacementHint)
	}
	// Todo 1 is marked done in GitLab. Inside the window nothing moves;
	// past it, the descent's targeted walk flips the label.
	src.pending = src.pending[1:]
	wk, _ = p.List(ctx, &pluginv1.ListRequest{Context: "week:2026-08-17"})
	if strings.HasPrefix(wk.Entries[0].Label, "✓") {
		t.Error("state changed inside the refresh window")
	}
	clock = clock.Add(DefaultRefresh + time.Second)
	wk, _ = p.List(ctx, &pluginv1.ListRequest{Context: "week:2026-08-17"})
	if src.calls != 4 || !strings.HasPrefix(wk.Entries[0].Label, "✓ !1 ") || wk.Entries[0].StatusDetail != todos.StateDone {
		t.Errorf("after the window: calls=%d entry=%v", src.calls, wk.Entries[0])
	}
	// The todo did not go away.
	if len(wk.Entries) != 2 {
		t.Errorf("a done todo left the listing: %v", wk.Entries)
	}
	root, _ = p.List(ctx, &pluginv1.ListRequest{Context: todos.RootContext})
	if root.Entries[1].Label != "2026-08-17 · 0 open · 2 done" {
		t.Errorf("root after flip = %q", root.Entries[1].Label)
	}
}

func TestServeContentAndProbe(t *testing.T) {
	src := &oneShot{pending: []todos.Todo{mk(1, "2026-08-18T10:00:00Z", "pending")}}
	p := New(src, nil, Options{})
	ctx := context.Background()
	if _, err := p.List(ctx, &pluginv1.ListRequest{Context: todos.RootContext}); err != nil {
		t.Fatal(err)
	}
	s := &sink{}
	if err := p.ServeContent(&pluginv1.ServeContentRequest{Key: "todo:1"}, s); err != nil {
		t.Fatal(err)
	}
	if c := s.chunks[0]; c.Status != 200 || !strings.HasPrefix(c.MediaType, "text/html") || !strings.Contains(string(c.Data), "<strong>review</strong>") || !strings.Contains(string(c.Data), "mr x") {
		t.Errorf("page = %d %s %s", c.Status, c.MediaType, c.Data)
	}
	s = &sink{}
	_ = p.ServeContent(&pluginv1.ServeContentRequest{Key: "todo:99"}, s)
	if c := s.chunks[0]; c.Status != 404 || !strings.Contains(string(c.Data), "todo:99") {
		t.Errorf("unknown todo = %d %s", c.Status, c.Data)
	}
	s = &sink{}
	_ = p.ServeContent(&pluginv1.ServeContentRequest{Key: "todo:1", Subpath: "x"}, s)
	if s.chunks[0].Status != 404 {
		t.Error("a subpath must 404")
	}
	probe := func(key string) pluginv1.ProbeResponse_Presence {
		r, _ := p.Probe(ctx, &pluginv1.ProbeRequest{Key: key})
		return r.Presence
	}
	if probe("todo:1") != pluginv1.ProbeResponse_PRESENCE_PRESENT || probe("todo:99") != pluginv1.ProbeResponse_PRESENCE_UNSPECIFIED ||
		probe("week:2026-08-17") != pluginv1.ProbeResponse_PRESENCE_PRESENT || probe("week:2026-01-05") != pluginv1.ProbeResponse_PRESENCE_UNSPECIFIED ||
		probe("junk") != pluginv1.ProbeResponse_PRESENCE_GONE {
		t.Error("probe verdicts: known PRESENT, unknown UNSPECIFIED (never GONE), malformed GONE")
	}
	res, _ := p.Search(ctx, &pluginv1.SearchRequest{Query: "REVIEW"})
	if len(res.Results) != 1 || res.Results[0].Entry.Key != "todo:1" || strings.Join(res.Results[0].ContextPath, "/") != "todos/week:2026-08-17" {
		t.Errorf("search = %v", res.Results)
	}
}

func TestNoTokenSurfacesAsAVerdict(t *testing.T) {
	p := New(nil, errors.New("token_file not configured"), Options{})
	_, err := p.List(context.Background(), &pluginv1.ListRequest{Context: todos.RootContext})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "token_file") {
		t.Errorf("err = %v, want FailedPrecondition naming the token", err)
	}
	if _, err := p.Info(context.Background(), &pluginv1.InfoRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("Info must refuse the handshake with the reason (the launch fails), got %v", err)
	}
	if _, err := p.List(context.Background(), &pluginv1.ListRequest{Context: "bogus"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("unknown context → %v", err)
	}
}
