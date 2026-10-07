// Package nodebuild is the client's half of "a client the node served runs
// the node's build" (Server.staleBuild): every call names this page's build,
// the door's refusal is heard on whichever carrier it arrives, and Decide
// says what the page does about it.
package nodebuild

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"sync"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/tracewire"
)

// Gate stamps this page's build on its calls and hears the refusal of it.
// onStale runs for every refusal heard, from any goroutine.
type Gate struct {
	build   string
	onStale func(node string)

	mu       sync.Mutex
	accepted bool
}

func New(build string, onStale func(node string)) *Gate {
	return &Gate{build: build, onStale: onStale}
}

func (g *Gate) Build() string { return g.build }

// Accepted reports whether the node has answered a call of this page, which
// is what tells a refusal a reload can fix from one it just failed to.
func (g *Gate) Accepted() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.accepted
}

// Hear takes one call's outcome; the shell socket's refusal arrives here as
// the node's build instead (shellws.Options.OnStaleBuild calls Stale).
func (g *Gate) Hear(err error) {
	if node, ok := gwerr.StaleBuildOf(err); ok {
		g.Stale(node)
		return
	}
	if err == nil || errors.Is(err, io.EOF) {
		g.mu.Lock()
		g.accepted = true
		g.mu.Unlock()
	}
}

func (g *Gate) Stale(node string) { g.onStale(node) }

// BeaconPath is path with this page's build in the query, because a beacon
// cannot carry a header.
func (g *Gate) BeaconPath(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + tracewire.BuildQuery + "=" + url.QueryEscape(g.build)
}

// Interceptor stamps every call and hears every answer.
func (g *Gate) Interceptor() connect.Interceptor { return interceptor{g} }

type interceptor struct{ g *Gate }

func (i interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !req.Spec().IsClient {
			return next(ctx, req)
		}
		req.Header().Set(tracewire.BuildHeader, i.g.build)
		resp, err := next(ctx, req)
		i.g.Hear(err)
		return resp, err
	}
}

func (i interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set(tracewire.BuildHeader, i.g.build)
		return &heardConn{StreamingClientConn: conn, g: i.g}
	}
}

func (i interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// heardConn hears a stream's verdict where a server stream delivers it, on
// Receive.
type heardConn struct {
	connect.StreamingClientConn
	g *Gate
}

func (h *heardConn) Receive(msg any) error {
	err := h.StreamingClientConn.Receive(msg)
	h.g.Hear(err)
	return err
}

// Verdict is what a page does when the door refuses its build.
type Verdict int

const (
	// Reload loads the node's own client, through the unload path every
	// reload takes; the URL holds the place, so it lands where it was.
	Reload Verdict = iota
	// Hold keeps the page, because it holds text the node never saved and
	// will no longer take from it: the bytes stay on screen to be copied.
	Hold
	// Stuck is a page a reload just served and the node still refuses: the
	// node is serving a client of another build, and another reload would
	// loop.
	Stuck
)

func (v Verdict) String() string {
	switch v {
	case Hold:
		return "hold"
	case Stuck:
		return "stuck"
	}
	return "reload"
}

// Decide is the one answer to the door's refusal. reloaded is whether a
// reload loaded this page, accepted whether the node has answered any of its
// calls, unsaved how many tiles hold text the node has not saved.
func Decide(reloaded, accepted bool, unsaved int) Verdict {
	switch {
	case reloaded && !accepted:
		return Stuck
	case unsaved > 0:
		return Hold
	}
	return Reload
}

// Notice is what the strip says for a verdict that keeps the page.
func Notice(v Verdict, node string) string {
	n := tracewire.ShortCommit(node)
	switch v {
	case Hold:
		return "gridwell was updated to " + n + " and this page can no longer save: copy your unsaved text, then reload"
	case Stuck:
		return "the node runs " + n + " but serves a client of another build: rebuild the client"
	}
	return ""
}
