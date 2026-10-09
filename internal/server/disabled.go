package server

// A source the user disabled (plugin.Registry.Disable) is read-only until the
// node restarts. The router is the one layer every verb crosses for plugins
// and connections alike, so it refuses the writes and stamps the reads here,
// deriving both from the registry on the way out; nothing is stored.

import (
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
)

// disabledReason is the sentence of the switched-off source owning id, ""
// while that source is on.
func (s *Server) disabledReason(id string) (ns, reason string) {
	ns = rpc.OwnerNamespaceOf(id, s.cfg.ID)
	return ns, s.pluginReg.DisabledReason(ns)
}

// refuseDisabled answers a write naming any of ids whose source is switched
// off: the verdict gwerr.SourceDisabled, which the client drops rather than
// retries.
func (rt *router) refuseDisabled(ids ...string) error {
	for _, id := range ids {
		if id == "" {
			continue
		}
		if ns, reason := rt.srv.disabledReason(id); reason != "" {
			return gwerr.SourceDisabled(ns, reason)
		}
	}
	return nil
}

// stampDisabled marks a grid answer from a switched-off source as taking no
// edits and no tiles, every row carrying the reason (Tile.read_only), so the
// client refuses an edit before it is typed. g and tiles are the router's own
// qualified copies.
func (s *Server) stampDisabled(gridID string, g *pb.Grid, tiles []*pb.Tile) {
	_, reason := s.disabledReason(gridID)
	if reason == "" {
		return
	}
	if g != nil {
		g.Writable, g.AcceptsTiles = false, proto.Bool(false)
	}
	for _, t := range tiles {
		t.ReadOnly = reason
	}
}

// stampDisabledTile is stampDisabled for one row, by its own id.
func (s *Server) stampDisabledTile(t *pb.Tile) {
	if t != nil {
		s.stampDisabled(t.GetId(), nil, []*pb.Tile{t})
	}
}

// announceDisabled tells every open stream that each grid of ns on screen
// changed, so the views showing one read it again and take the stamp.
func (s *Server) announceDisabled(ns string) {
	for _, id := range s.interest.Union() {
		if rpc.OwnerNamespaceOf(id, s.cfg.ID) == ns {
			s.switchedOff.Publish(&pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: id}}})
		}
	}
}
