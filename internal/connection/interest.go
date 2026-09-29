package connection

// The transport is one client of each far node: what this node's clients
// show there is cut by connection and told to that node under the
// connection's own session, so a far plugin watches what is on screen here.

import (
	"context"
	"crypto/rand"
	"log"
	"slices"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// tellFarWait bounds one SetInterest to a far node, so a far node that stopped
// answering cannot hold a newer set behind it.
var tellFarWait = 10 * time.Second

// farInterest is one connection's share and the session it rides, which is
// also the session of the connection's event stream (fanInRemote): the far
// node counts the share while that stream is open.
type farInterest struct {
	session string
	grids   []string // the far node's own ids; guarded by Server.mu
	kick    chan struct{}
}

func (f *farInterest) poke() {
	select {
	case f.kick <- struct{}{}:
	default:
	}
}

// farOf is name's share, made on first use.
func (s *Server) farOf(name string) *farInterest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.farLocked(name)
}

func (s *Server) farLocked(name string) *farInterest {
	if s.far == nil {
		s.far = map[string]*farInterest{}
	}
	f, ok := s.far[name]
	if !ok {
		f = &farInterest{session: rand.Text(), kick: make(chan struct{}, 1)}
		s.far[name] = f
	}
	return f
}

// SetInterest takes the transport's whole share, "<conn>/<far id>" each, and
// passes each connection its part. It never dials: a share waits for the
// connection's transport, whose tellFar sends it.
func (s *Server) SetInterest(_ context.Context, req *gridwellv1.SetInterestRequest) (*gridwellv1.SetInterestResponse, error) {
	shares := map[string][]string{}
	for _, id := range req.GetGridIds() {
		if first, rest, ok := rpc.SplitID(id); ok && !rpc.IsTileSegment(first) {
			shares[first] = append(shares[first], rest)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name := range shares {
		s.farLocked(name)
	}
	for name, f := range s.far {
		if g := shares[name]; !slices.Equal(g, f.grids) {
			f.grids = g
			f.poke()
		}
	}
	return &gridwellv1.SetInterestResponse{}, nil
}

// tellFar sends a connection's share for as long as its transport lives, on
// every change and every re-open of the far stream, since the far node
// forgets a session whose stream closed. An empty share is sent only to
// retract one the far node may hold. A far node from before interest answers
// Unimplemented and watches as it always did.
func (s *Server) tellFar(ctx context.Context, name string, client namespace.Namespace) {
	f := s.farOf(name)
	told := false
	for {
		s.mu.Lock()
		grids := slices.Clone(f.grids)
		s.mu.Unlock()
		if len(grids) > 0 || told {
			cctx, cancel := context.WithTimeout(ctx, tellFarWait)
			_, err := client.SetInterest(cctx, &gridwellv1.SetInterestRequest{Session: f.session, GridIds: grids})
			cancel()
			told = len(grids) > 0
			if err != nil && ctx.Err() == nil && status.Code(err) != codes.Unimplemented && !gwerr.IsTransport(err) {
				log.Printf("gridwell: connection %s: interest refused: %v", name, err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-f.kick:
		}
	}
}
