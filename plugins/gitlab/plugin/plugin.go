// Package provider is the gitlab todos CONTENT PROVIDER: the wire half
// over plugins/gitlab/todos. Contexts: the root ("todos") lists weeks;
// a week ("week:<monday>") lists the todos created that week as page
// tiles serving their own HTML. Keys are GitLab's todo ids, stable
// forever. Listings are NON-authoritative and Probe never answers GONE:
// a todo never disappears from the grid — it changes state (done) when
// refreshed, and the node's read-through cache remembers it across
// provider restarts (the provider itself is stateless by contract).
package plugin

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/plugins/gitlab/todos"
)

// Kind is the provider's declared kind — and the binary suffix
// (gridwell-plugin-gitlab).
const Kind = "gitlab"

// DefaultRefresh bounds how often one context re-walks GitLab: the node
// lists a context on EVERY GetGrid/GetTile, and a descent must feel
// instant, not cost a round of API pages each time.
const DefaultRefresh = 30 * time.Second

// Plugin implements pluginv1.PluginServer.
type Plugin struct {
	pluginv1.UnimplementedPluginServer
	src     todos.Source
	srcErr  error // a configuration verdict (no token): every listing refuses with it
	mem     *todos.Memory
	refresh time.Duration
	now     func() time.Time
	label   string

	mu       sync.Mutex
	syncedAt map[string]time.Time // context → last successful walk
}

// Options tunes a provider. Zero values take the defaults.
type Options struct {
	Refresh time.Duration
	Now     func() time.Time
	Label   string // DisplayName; "" = "gitlab todos"
}

// New builds a provider over src. srcErr, when non-nil, is the reason
// there is no source (an unconfigured token): Info still answers, so
// the plugin is listed, and every listing refuses with the reason —
// the error surfaces instead of an empty grid.
func New(src todos.Source, srcErr error, o Options) *Plugin {
	p := &Plugin{src: src, srcErr: srcErr, mem: todos.NewMemory(), refresh: o.Refresh, now: o.Now, label: o.Label, syncedAt: map[string]time.Time{}}
	if p.refresh <= 0 {
		p.refresh = DefaultRefresh
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.label == "" {
		p.label = "gitlab todos"
	}
	return p
}

func (p *Plugin) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	// A plugin without the config it needs refuses its HANDSHAKE, so the
	// launch fails with the reason instead of serving an empty grid
	// (owner decision 2026-08-27).
	if p.srcErr != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "gitlab plugin: %v", p.srcErr)
	}
	return &pluginv1.InfoResponse{
		Kind:        Kind,
		DisplayName: p.label,
		RootContext: todos.RootContext,
	}, nil
}

// fresh reports whether ctxKey was walked within the refresh window —
// a root walk covers every week, so a week is fresh under either.
func (p *Plugin) fresh(ctxKey string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for _, k := range []string{ctxKey, todos.RootContext} {
		if t, ok := p.syncedAt[k]; ok && now.Sub(t) < p.refresh {
			return true
		}
	}
	return false
}

// sync walks GitLab for ctxKey unless it is fresh. since is zero for
// the root (everything) and the Monday for a week (targeted).
func (p *Plugin) sync(ctx context.Context, ctxKey string, since time.Time) error {
	if p.srcErr != nil {
		return status.Errorf(codes.FailedPrecondition, "gitlab provider: %v", p.srcErr)
	}
	if p.fresh(ctxKey) {
		return nil
	}
	if err := p.mem.Sync(ctx, p.src, since); err != nil {
		return err
	}
	p.mu.Lock()
	p.syncedAt[ctxKey] = p.now()
	p.mu.Unlock()
	return nil
}

// List answers the root (weeks) or one week (todos). A walk failure
// with a transport-shaped code degrades at the node to the remembered
// listing, stamped stale; a verdict (bad token) surfaces.
func (p *Plugin) List(ctx context.Context, req *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	switch {
	case req.Context == todos.RootContext:
		if err := p.sync(ctx, req.Context, time.Time{}); err != nil {
			return nil, err
		}
		weeks := p.mem.Weeks()
		open, done := 0, 0
		for _, w := range weeks {
			open += w.Open
			done += w.Done
		}
		// The totals ride the grid's source label, so the root says at a
		// glance what the walk found.
		return &pluginv1.ListResponse{Entries: todos.RootEntries(weeks), Authoritative: false,
			SourceLabel: fmt.Sprintf("%s · %d open · %d done", p.label, open, done)}, nil
	default:
		start, ok := todos.ParseWeekKey(req.Context)
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "gitlab provider: unknown context %q", req.Context)
		}
		if err := p.sync(ctx, req.Context, start); err != nil {
			return nil, err
		}
		return &pluginv1.ListResponse{Entries: todos.WeekEntries(start, p.mem.Week(start)), Authoritative: false, SourceLabel: req.Context}, nil
	}
}

// ReadContent answers the todo's markdown (Markdown): the text tile's
// face and rendered document, whose target link opens an ephemeral
// visit. An unknown key reads as a one-line notice.
func (p *Plugin) ReadContent(req *pluginv1.ReadContentRequest, stream pluginv1.Plugin_ReadContentServer) error {
	id, ok := todos.ParseKey(req.Key)
	if !ok {
		return stream.Send(&pluginv1.ContentChunk{}) // a week key: no body
	}
	t, known := p.mem.Get(id)
	if !known {
		return stream.Send(&pluginv1.ContentChunk{Data: todos.GoneMarkdown(req.Key), MediaType: "text/markdown"})
	}
	return stream.Send(&pluginv1.ContentChunk{Data: todos.Markdown(&t), MediaType: "text/markdown"})
}

// Probe never says GONE: a remembered todo is PRESENT; one this process
// has not seen is UNSPECIFIED — "cannot say", which keeps the node's
// remembered tile (I12). Todos do not magically go away.
func (p *Plugin) Probe(_ context.Context, req *pluginv1.ProbeRequest) (*pluginv1.ProbeResponse, error) {
	if id, ok := todos.ParseKey(req.Key); ok {
		if _, known := p.mem.Get(id); known {
			return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_PRESENT}, nil
		}
		return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_UNSPECIFIED}, nil
	}
	if start, ok := todos.ParseWeekKey(req.Key); ok {
		if len(p.mem.Week(start)) > 0 {
			return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_PRESENT}, nil
		}
		return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_UNSPECIFIED}, nil
	}
	return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_GONE}, nil
}

// Search matches the query against titles, refs, bodies, projects and
// authors of every remembered todo; each result's path is root → week.
func (p *Plugin) Search(_ context.Context, req *pluginv1.SearchRequest) (*pluginv1.SearchResponse, error) {
	q := strings.ToLower(strings.TrimSpace(req.Query))
	if q == "" {
		return &pluginv1.SearchResponse{}, nil
	}
	limit := int(req.Limit)
	if limit <= 0 {
		limit = 50
	}
	resp := &pluginv1.SearchResponse{}
	all := p.mem.All()
	for i := len(all) - 1; i >= 0 && len(resp.Results) < limit; i-- { // newest first
		t := all[i]
		hay := strings.ToLower(strings.Join([]string{t.Label(), t.Body, t.Project.PathWithNamespace, t.Author.Name, t.Author.Username}, "\n"))
		if !strings.Contains(hay, q) {
			continue
		}
		start := todos.WeekStart(t.CreatedAt)
		entries := todos.WeekEntries(start, []todos.Todo{t})
		if len(entries) == 0 {
			continue
		}
		resp.Results = append(resp.Results, &pluginv1.SearchResult{
			Entry:       entries[0],
			ContextPath: []string{todos.RootContext, todos.WeekKey(start)},
			Snippet:     t.Action(),
			Score:       1,
		})
	}
	return resp, nil
}
