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
	"strconv"
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

// Verdict is what a page does when the door refuses its build. It never
// reloads itself: only the user's press on the notice's button does
// (errsurface.Reload).
type Verdict int

const (
	// Offer keeps the page and offers the reload in one notice that names
	// what reloading will cost.
	Offer Verdict = iota
	// Stuck is a page a reload just served and the node still refuses: the
	// node is serving a client of another build, and another reload would
	// fail the same way.
	Stuck
)

func (v Verdict) String() string {
	if v == Stuck {
		return "stuck"
	}
	return "offer"
}

// Decide is the one answer to the door's refusal. reloaded is whether a
// reload loaded this page, accepted whether the node has answered any of its
// calls.
func Decide(reloaded, accepted bool) Verdict {
	if reloaded && !accepted {
		return Stuck
	}
	return Offer
}

// Updated opens the notice that offers the reload.
const Updated = "Gridwell was updated — reload to continue."

// Notice is what the strip says for a verdict. Offer's names what reloading
// will cost: the parked writes the door refused, given their outbox ops in
// drain order (Unsaved), and, when unsavedText tiles hold text the node never
// saved, that it should be copied first.
func Notice(v Verdict, node string, ops []string, unsavedText int) string {
	if v == Stuck {
		return "the node runs " + tracewire.ShortCommit(node) + " but serves a client of another build: rebuild the client"
	}
	msg := Updated
	if u := Unsaved(ops); u != "" {
		msg += " " + u + "."
	}
	if unsavedText > 0 {
		msg += " Copy your text first."
	}
	return msg
}

// kinds names a parked write by its outbox op; an op missing here is named
// by itself.
var kinds = map[string][2]string{
	"Content":         {"text edit", "text edits"},
	"Rename":          {"rename", "renames"},
	"ConfigureURL":    {"address", "addresses"},
	"PlaceTile":       {"layout change", "layout changes"},
	"PaneLayout":      {"layout change", "layout changes"},
	"DeleteTile":      {"delete", "deletes"},
	"SetFraming":      {"view change", "view changes"},
	"SetContentZoom":  {"view change", "view changes"},
	"SetTextView":     {"view change", "view changes"},
	"SetURLState":     {"page capture", "page captures"},
	"SetFrozen":       {"page capture", "page captures"},
	"SetShellPreview": {"shell capture", "shell captures"},
}

// Unsaved names the parked writes the door refused, by kind and count, "" for
// none. They cannot be sent again: they are this build's, and the door takes
// only the node's.
func Unsaved(ops []string) string {
	var order [][2]string
	counts := map[[2]string]int{}
	for _, op := range ops {
		k, ok := kinds[op]
		if !ok {
			k = [2]string{op, op}
		}
		if counts[k] == 0 {
			order = append(order, k)
		}
		counts[k]++
	}
	if len(order) == 0 {
		return ""
	}
	parts := make([]string, len(order))
	for i, k := range order {
		name := k[0]
		if counts[k] > 1 {
			name = k[1]
		}
		parts[i] = strconv.Itoa(counts[k]) + " " + name
	}
	list := parts[len(parts)-1]
	if len(parts) > 1 {
		list = strings.Join(parts[:len(parts)-1], ", ") + " and " + list
	}
	verb := " were"
	if len(ops) == 1 {
		verb = " was"
	}
	return list + verb + " not saved"
}
