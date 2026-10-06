package server

// Interest crosses the router in two halves: a client's set arrives under its
// session and is booked (interest.Book owns the union), and each new union is
// cut by owner and handed to every namespace as its whole share.

import (
	"context"
	"strings"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/trace"
)

// SetInterest books one session's set. It has no id to route: every grid in
// it is cut to its owner when the union moves.
func (rt *router) SetInterest(_ context.Context, req *pb.SetInterestRequest) (*pb.SetInterestResponse, error) {
	if req.GetSession() == "" {
		return nil, status.Error(gcodes.InvalidArgument, "SetInterest names no session")
	}
	rt.srv.interest.Set(req.GetSession(), req.GetGridIds())
	return &pb.SetInterestResponse{}, nil
}

// spreadInterest hands every namespace its share of union, an empty one
// included, since a share that emptied must stop being watched. A grid whose
// namespace this node does not declare is nobody's to watch.
func (s *Server) spreadInterest(union []string) {
	trace.Emit("interest", "union", strings.Join(union, " "), nil)
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
		_, err := n.NS.SetInterest(context.Background(), &pb.SetInterestRequest{GridIds: shares[owner{n.UUID, n.Transit}]})
		if err != nil && !isUnimplemented(err) {
			trace.Emit("interest", "refused", err.Error(), map[string]string{"ns": n.UUID})
		}
	}
}
