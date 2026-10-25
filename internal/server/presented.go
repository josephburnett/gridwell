package server

// A url tile whose address a route sends to a plugin that presents it is
// presented by that plugin (Tile.presented_by). The node derives it on every
// answer leaving the router for a row it holds, home's or a plugin's, from
// server.yaml's routes and the presents each routed plugin's Info declares;
// a far row arrives with its own node's verdict. Nothing is stored.

import (
	"context"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// stampPresented sets presented_by on each url tile with an address of its
// own. The presents are asked once per answer, and only when a tile needs
// them; a plugin whose Info does not answer presents nothing.
func (s *Server) stampPresented(ctx context.Context, tiles ...*pb.Tile) {
	var presents map[string][]string
	for _, t := range tiles {
		if !rpc.WebContent(t) || t.UrlString == "" || len(s.cfg.Routes) == 0 {
			continue
		}
		if presents == nil {
			presents = map[string][]string{}
			for _, r := range s.cfg.Routes {
				if _, asked := presents[r.Plugin]; r.Plugin != "" && !asked {
					info, _ := s.pluginInfo(ctx, r.Plugin)
					presents[r.Plugin] = info.GetPresents()
				}
			}
		}
		t.PresentedBy = rpc.Decide(t.UrlString, s.cfg.Routes, presents, rpc.MatchAddress).Plugin
	}
}
