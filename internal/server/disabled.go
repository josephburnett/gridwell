package server

// A connection the user disabled (Sources.Switches) is read-only until the
// node restarts. The router is the one layer every verb crosses, so it
// refuses the writes and stamps the reads here, deriving both from the
// switches on the way out; nothing is stored.

import (
	"strings"

	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
)

// disabledReason is the sentence of the switched-off connection owning id,
// "" while it is on and for every other source.
func (s *Server) disabledReason(id string) (ns, reason string) {
	ns = rpc.OwnerNamespaceOf(id, s.cfg.ID)
	return ns, s.nsDisabledReason(ns)
}

// nsDisabledReason is disabledReason for an owner id.
func (s *Server) nsDisabledReason(ns string) string {
	if name, ok := s.connectionOf(ns); ok {
		return s.src.Switches.DisabledReason(name)
	}
	return ""
}

// disabledNow is every connection the user disabled, by its namespace.
func (s *Server) disabledNow() []string {
	if s.src.Switches == nil {
		return nil
	}
	var out []string
	for _, name := range s.src.Switches.DisabledNow() {
		out = append(out, rpc.QualifyID(s.cfg.ID, name))
	}
	return out
}

// connectionOf is the connection a namespace of this node's names, "<id>/<name>";
// ok is false for home, the files and anything a far node declares, none of
// which this node can switch off.
func (s *Server) connectionOf(ns string) (name string, ok bool) {
	first, rest, split := rpc.SplitID(ns)
	ok = split && s.src.Switches != nil && first == s.cfg.ID && !strings.Contains(rest, "/") &&
		rpc.ShapeOf(rest) == rpc.ShapeNamespace
	return rest, ok
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
// edits, no tiles and no move, every row carrying the reason (Tile.read_only), so the
// client refuses an edit before it is typed. g and tiles are the router's own
// qualified copies.
func (s *Server) stampDisabled(gridID string, g *pb.Grid, tiles []*pb.Tile) {
	_, reason := s.disabledReason(gridID)
	if reason == "" {
		return
	}
	if g != nil {
		g.Writable, g.AcceptsTiles, g.Moves = false, proto.Bool(false), proto.Bool(false)
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
	for _, id := range s.subscription.Union() {
		if rpc.OwnerNamespaceOf(id, s.cfg.ID) == ns {
			s.switchedOff.Publish(&pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: id}}})
		}
	}
}
