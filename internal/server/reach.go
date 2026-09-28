package server

import (
	"context"
	"errors"
	"fmt"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A reference written into another node's grid is spelled here, from that
// node, over the connection graph this node can see (rpc.Reach), or refused.

// maxReachHops bounds both the graph walk and a spelled route, so a cycle of
// connections or a sprawling mesh costs a bounded number of handshakes.
const (
	maxReachHops  = 8
	maxReachNodes = 64
)

// spellReferences rewrites t's references into the frame of the node holding
// grid, where rpc.HeldAcross says they part from it; the graph is fetched only
// then. A reference the holder has no route to is refused, and nothing is
// written.
func (rt *router) spellReferences(ctx context.Context, grid string, t *pb.Tile) error {
	if t == nil {
		return nil
	}
	me := rt.srv.cfg.ID
	var reach rpc.Reach
	for _, ref := range []*string{&t.ChildGridId, &t.LinkTargetId} {
		if !rpc.HeldAcross(grid, *ref, me) {
			continue
		}
		if reach == nil {
			var err error
			if reach, err = rt.reach(ctx); err != nil {
				return err
			}
		}
		spelled, err := reach.Respell(me, grid, *ref, maxReachHops)
		var u *rpc.Unreachable
		switch {
		case errors.As(err, &u) && u.Holder == "":
			return status.Errorf(gcodes.Unavailable, "cannot write a link into %s: the way to the node holding it is not answering", grid)
		case errors.As(err, &u):
			// InvalidArgument, not FailedPrecondition: the client reads the
			// latter as a version race and refetches silently, and this
			// refusal must reach the strip.
			target := reachLabel(reach, u.Target)
			if u.Target == "" {
				target = *ref
			}
			return status.Errorf(gcodes.InvalidArgument, "%s has no connection to %s", reachLabel(reach, u.Holder), target)
		case err != nil:
			return err
		}
		*ref = spelled
	}
	return nil
}

func reachLabel(r rpc.Reach, node string) string {
	if n, ok := r[node]; ok && n.Label != "" {
		return n.Label
	}
	return node
}

// reach walks the handshakes of every node this one can see, breadth first
// and bounded. A node is labelled as this node names the first connection
// found to it, and this node by its home row. A node whose handshake does not
// answer is in the graph with no connections of its own.
func (rt *router) reach(ctx context.Context) (rpc.Reach, error) {
	me := rt.srv.cfg.ID
	lp, err := rt.Handshake(ctx, &pb.HandshakeRequest{})
	if err != nil {
		return nil, fmt.Errorf("read this node's connections: %w", err)
	}
	label := me
	if h := rpc.HomeRow(lp); h != nil && h.Label != "" {
		label = h.Label
	}
	r := rpc.Reach{me: {Label: label}}
	type visit struct {
		node string
		lp   *pb.HandshakeResponse
	}
	queue := []visit{{me, lp}}
	for depth := 0; len(queue) > 0 && depth < maxReachHops; depth++ {
		var next []visit
		for _, v := range queue {
			for _, row := range v.lp.GetPlugins() {
				if !rpc.IsConnectionRow(row) {
					continue
				}
				edges := rpc.ReachEdges(&pb.HandshakeResponse{Plugins: []*pb.PluginInfo{row}})
				if len(edges) == 0 {
					continue
				}
				e := edges[0]
				r[v.node].Edges = append(r[v.node].Edges, e)
				if _, seen := r[e.Lands]; seen || len(r) >= maxReachNodes {
					continue
				}
				r[e.Lands] = &rpc.ReachNode{Label: e.Label}
				hctx, cancel := context.WithTimeout(ctx, pluginInfoTimeout)
				far, err := rt.Handshake(hctx, &pb.HandshakeRequest{Namespace: row.Uuid})
				cancel()
				if err == nil {
					next = append(next, visit{e.Lands, far})
				}
			}
		}
		queue = next
	}
	return r, nil
}
