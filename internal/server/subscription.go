package server

// A view crosses the router in two halves: a client's view arrives under its
// session and is booked (subscription.Book owns the views and the union), and each
// new union is cut by owner and handed to every namespace as its whole share.

import (
	"context"
	"strings"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/subscription"
	"github.com/josephburnett/gridwell/internal/trace"
)

// Session is one client's view as the node holds it; see subscription.Session.
type Session = subscription.Session

// Sessions is each session that has said what it shows, open or away; see
// subscription.Book.Sessions.
func (s *Server) Sessions() []Session { return s.subscription.Sessions() }

// SetView books one session's view. It has no id to route: every grid it shows
// is cut to its owner when the union moves.
func (rt *router) SetView(_ context.Context, req *pb.SetViewRequest) (*pb.SetViewResponse, error) {
	if req.GetSession() == "" {
		return nil, status.Error(gcodes.InvalidArgument, "SetView names no session")
	}
	trace.Emit("view", "set", viewLine(req), map[string]string{
		"session": req.GetSession(), "label": req.GetLabel(), "levels": strings.Join(req.GetPaneTiles(), " "),
	})
	rt.srv.subscription.Set(req.GetSession(), req)
	return &pb.SetViewResponse{}, nil
}

// viewLine is a view's panes for the trace, one word each: id=grid, then
// +content when inside one, and * on the focused pane.
func viewLine(v *pb.SetViewRequest) string {
	words := make([]string, 0, len(v.GetPanes()))
	for _, p := range v.GetPanes() {
		w := p.GetId() + "=" + p.GetGridId()
		if c := p.GetContentId(); c != "" {
			w += "+" + c
		}
		if p.GetFocused() {
			w += "*"
		}
		words = append(words, w)
	}
	return strings.Join(words, " ")
}

// spreadSubscription hands every namespace its share of union, an empty one
// included, since a share that emptied must stop being watched. A grid whose
// namespace this node does not declare is nobody's to watch.
func (s *Server) spreadSubscription(union []string) {
	trace.Emit("view", "union", strings.Join(union, " "), nil)
	type owner struct {
		uuid    string
		transit bool
	}
	shares := map[owner][]string{}
	for _, id := range union {
		if _, local, uuid, transit, ok := s.resolve(id); ok {
			shares[owner{uuid, transit}] = append(shares[owner{uuid, transit}], local)
		}
	}
	// A refusal is the namespace's own news, on its stream (namespace.Namespace).
	for _, n := range s.namespaces() {
		_, err := n.NS.SetView(context.Background(), &pb.SetViewRequest{ShownGridIds: shares[owner{n.UUID, n.Transit}]})
		if err != nil && !isUnimplemented(err) {
			trace.Emit("view", "refused", err.Error(), map[string]string{"ns": n.UUID})
		}
	}
}
