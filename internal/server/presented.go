package server

// A url tile whose address a route sends to a plugin that presents it is
// presented by that plugin (Tile.presented_by). The node derives it on every
// answer leaving the router for a row it holds, home's or a plugin's, from
// server.yaml's routes and the presents each routed plugin's Info declares;
// a far row arrives with its own node's verdict. Nothing is stored.

import (
	"context"
	"strings"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// stampPresented sets presented_by on each url tile with an address of its
// own. The presents are asked once per answer, and only when a tile needs
// them.
func (s *Server) stampPresented(ctx context.Context, tiles ...*pb.Tile) {
	var presents map[string][]string
	for _, t := range tiles {
		if !rpc.WebContent(t) || t.UrlString == "" || len(s.cfg.Routes) == 0 {
			continue
		}
		if presents == nil {
			presents = s.routedPresents(ctx)
		}
		t.PresentedBy = rpc.Decide(t.UrlString, s.cfg.Routes, presents, rpc.MatchAddress).Plugin
	}
}

// routedPresents is what each plugin a route names presents, from its Info;
// a plugin whose Info does not answer presents nothing.
func (s *Server) routedPresents(ctx context.Context) map[string][]string {
	presents := map[string][]string{}
	for _, r := range s.cfg.Routes {
		if _, asked := presents[r.Plugin]; r.Plugin != "" && !asked {
			info, _ := s.pluginInfo(ctx, r.Plugin)
			presents[r.Plugin] = info.GetPresents()
		}
	}
	return presents
}

// leavesPage is the site address a request under row's page names when it is
// a document (a navigation) and the routes do not send that address to the
// plugin presenting row. The plugin's assets, a sub-resource and a far row,
// which its own node judges, never leave.
func (s *Server) leavesPage(ctx context.Context, row *pb.Tile, req *pb.ServeContentRequest) (string, bool) {
	if row == nil || row.PresentedBy == "" || !document(req.Headers) {
		return "", false
	}
	location := req.Subpath
	if req.Query != "" {
		location += "?" + req.Query
	}
	site, ok := rpc.PresentedSite("", row.UrlString, location)
	if !ok || rpc.Decide(site, s.cfg.Routes, s.routedPresents(ctx), rpc.MatchAddress).Plugin == row.PresentedBy {
		return "", false
	}
	return site, true
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
