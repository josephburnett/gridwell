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

// Provider implements pluginv1.PluginServer.
type Provider struct {
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
func New(src todos.Source, srcErr error, o Options) *Provider {
	p := &Provider{src: src, srcErr: srcErr, mem: todos.NewMemory(), refresh: o.Refresh, now: o.Now, label: o.Label, syncedAt: map[string]time.Time{}}
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

func (p *Provider) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{
		Kind:        Kind,
		DisplayName: p.label,
		RootContext: todos.RootContext,
	}, nil
}

// fresh reports whether ctxKey was walked within the refresh window —
// a root walk covers every week, so a week is fresh under either.
func (p *Provider) fresh(ctxKey string) bool {
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
func (p *Provider) sync(ctx context.Context, ctxKey string, since time.Time) error {
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
func (p *Provider) List(ctx context.Context, req *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
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

// pageFor answers a key's page bytes and HTTP status.
func (p *Provider) pageFor(key string) (data []byte, code int64) {
	id, ok := todos.ParseKey(key)
	if !ok {
		return nil, 404
	}
	t, ok := p.mem.Get(id)
	if !ok {
		return todos.GonePage(key), 404
	}
	return todos.Page(&t), 200
}

// ReadContent answers the page source as text/html (what fs answers
// for an .html file): a page tile's document body IS its page.
func (p *Provider) ReadContent(req *pluginv1.ReadContentRequest, stream pluginv1.Plugin_ReadContentServer) error {
	data, code := p.pageFor(req.Key)
	if code != 200 {
		return stream.Send(&pluginv1.ContentChunk{})
	}
	return stream.Send(&pluginv1.ContentChunk{Data: data, MediaType: "text/html; charset=utf-8"})
}

func (p *Provider) ServeContent(req *pluginv1.ServeContentRequest, stream pluginv1.Plugin_ServeContentServer) error {
	if req.Subpath != "" {
		return stream.Send(&pluginv1.ServeContentChunk{Status: 404, MediaType: "text/plain", Data: []byte("not found")})
	}
	data, code := p.pageFor(req.Key)
	if data == nil {
		data = []byte("not found")
		return stream.Send(&pluginv1.ServeContentChunk{Status: code, MediaType: "text/plain", Data: data})
	}
	return stream.Send(&pluginv1.ServeContentChunk{Status: code, MediaType: "text/html; charset=utf-8", Data: data})
}

// GetPreview is the tile face: a rendered card for a remembered todo,
// nothing for anything else (the client keeps showing the label).
func (p *Provider) GetPreview(_ context.Context, req *pluginv1.GetPreviewRequest) (*pluginv1.GetPreviewResponse, error) {
	if id, ok := todos.ParseKey(req.Key); ok {
		if t, known := p.mem.Get(id); known {
			return &pluginv1.GetPreviewResponse{Jpeg: todos.Preview(&t)}, nil
		}
	}
	return &pluginv1.GetPreviewResponse{}, nil
}

// Probe never says GONE: a remembered todo is PRESENT; one this process
// has not seen is UNSPECIFIED — "cannot say", which keeps the node's
// remembered tile (I12). Todos do not magically go away.
func (p *Provider) Probe(_ context.Context, req *pluginv1.ProbeRequest) (*pluginv1.ProbeResponse, error) {
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
func (p *Provider) Search(_ context.Context, req *pluginv1.SearchRequest) (*pluginv1.SearchResponse, error) {
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
