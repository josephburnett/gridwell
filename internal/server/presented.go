package server

// A url tile whose address a route sends to a plugin that presents it is
// presented by that plugin (Tile.presented_by), and broken (broken_reason)
// while that plugin's declaration is unknown. The node derives both on every
// answer leaving the router for a row it holds, home's or a plugin's, from
// server.yaml's routes and what each routed plugin declares (rpc.Decide); a
// far row arrives with its own node's verdict. Nothing is stored.

import (
	"context"
	"strings"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// stampPresented sets presented_by and broken_reason on each url tile with an
// address of its own. The declarations are read once per answer, and only
// when a tile needs them.
func (s *Server) stampPresented(ctx context.Context, tiles ...*pb.Tile) {
	var declared map[string]rpc.Declared
	for _, t := range tiles {
		if !rpc.WebContent(t) || t.UrlString == "" || len(s.cfg.Routes) == 0 {
			continue
		}
		if declared == nil {
			declared = s.routedDeclared(ctx)
		}
		v := rpc.Decide(t.UrlString, s.cfg.Routes, declared)
		t.PresentedBy, t.BrokenReason = v.Plugin, v.Broken
	}
}

// routedDeclared is what each plugin a route names declares, read as the
// handshake reads its row (rpc.DeclaredBy). One the node does not declare has
// no row, so its route falls through as the handshake's does.
func (s *Server) routedDeclared(ctx context.Context) map[string]rpc.Declared {
	declared := map[string]rpc.Declared{}
	for _, r := range s.cfg.Routes {
		if _, asked := declared[r.Plugin]; r.Plugin == "" || asked {
			continue
		}
		if _, ok := s.routeClient(r.Plugin); !ok {
			continue
		}
		row := &pb.PluginInfo{Unanswered: s.unanswered(r.Plugin)}
		if row.Unanswered == "" {
			info, err := s.pluginInfo(ctx, r.Plugin)
			row = buildPluginInfo(r.Plugin, "", "", info, err)
		}
		declared[r.Plugin] = rpc.DeclaredBy(row)
	}
	return declared
}

// unanswered is why plugin uuid does not answer now, "" while it does: the
// user's switch, or the adapter's state (pluginhost.Adapter.Answering),
// which asks nothing and probes, bounded by pluginInfoTimeout, a plugin not
// answering. Home holds no such state and answers whoever asks it.
func (s *Server) unanswered(uuid string) string {
	if reason := s.pluginReg.DisabledReason(uuid); reason != "" {
		return reason
	}
	c, _ := s.routeClient(uuid)
	if a, ok := c.(interface{ Answering(time.Duration) string }); ok {
		return a.Answering(pluginInfoTimeout)
	}
	return ""
}

// leavesPage is the site address a request under row's page names when it is
// a document (a navigation) and the routes do not send that address to the
// plugin presenting row, and the routes' verdict on it. The plugin's assets, a
// sub-resource and a far row, which its own node judges, never leave.
func (s *Server) leavesPage(ctx context.Context, row *pb.Tile, req *pb.ServeContentRequest) (string, rpc.Verdict, bool) {
	if row == nil || row.PresentedBy == "" || !document(req.Headers) {
		return "", rpc.Verdict{}, false
	}
	location := req.Subpath
	if req.Query != "" {
		location += "?" + req.Query
	}
	site, ok := rpc.PresentedSite("", row.UrlString, location)
	if !ok {
		return "", rpc.Verdict{}, false
	}
	v := rpc.Decide(site, s.cfg.Routes, s.routedDeclared(ctx))
	return site, v, v.Plugin != row.PresentedBy || v.Broken != ""
}

// document reports a request for a page to show, as its fetch metadata names
// it. A browser sends none to a page that is no secure context, and then a
// document is the request that accepts HTML, which an asset or a fetch does
// not by default.
func document(headers []*pb.Header) bool {
	dest, accept := "", ""
	for _, h := range headers {
		switch {
		case strings.EqualFold(h.Name, "Sec-Fetch-Dest"):
			dest = h.Value
		case strings.EqualFold(h.Name, "Accept"):
			accept = h.Value
		}
	}
	if dest != "" {
		return dest == "document"
	}
	return strings.Contains(accept, "text/html")
}
