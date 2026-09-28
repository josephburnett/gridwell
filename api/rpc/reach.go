package rpc

import (
	"fmt"
	"strings"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// A link is an id held on one node, and that node resolves only its first
// segment. So a reference written into another node's grid must be spelled
// from that node, along connections it declares; connections are one-way, and
// a node with no chain of them to the target cannot hold the link at all.

// Reach is the connection graph one node can see: every node reachable from
// it, by id, with the connections each declares and where each lands.
type Reach map[string]*ReachNode

// ReachNode is one node's declared connections, in declaration order.
type ReachNode struct {
	Label string
	Edges []ReachEdge
}

// ReachEdge is one connection: its name on the declaring node and the node it
// lands on.
type ReachEdge struct {
	Conn, Lands, Label string
}

// ReachEdges reads a handshake's connection rows as edges, in whatever frame
// the handshake was asked in: a row's landing is the segment its root adds
// after its own uuid. A row that never learned its landing is no edge.
func ReachEdges(lp *pb.HandshakeResponse) []ReachEdge {
	var out []ReachEdge
	for _, p := range lp.GetPlugins() {
		if !IsConnectionRow(p) {
			continue
		}
		rest, ok := strings.CutPrefix(p.GetRootGridId(), p.GetUuid()+"/")
		if !ok {
			continue
		}
		lands, _, _ := strings.Cut(rest, "/")
		conn := p.GetUuid()
		if i := strings.LastIndexByte(conn, '/'); i >= 0 {
			conn = conn[i+1:]
		}
		out = append(out, ReachEdge{Conn: conn, Lands: lands, Label: p.GetLabel()})
	}
	return out
}

// ChainOf splits an id in nodeID's frame into the connections it hops, the
// segments those hops take (prefix), and the rest (tail), which is the id in
// the frame of the node the hops end on: id == prefix + tail. A hop is a node
// segment followed by a namespace segment, since a plugin's own ids are rows
// or keys.
func ChainOf(id, nodeID string) (conns []string, prefix, tail string) {
	segs := strings.Split(id, "/")
	pos := 0
	for pos+2 < len(segs) && ShapeOf(segs[pos+1]) == ShapeNamespace {
		if pos == 0 && (nodeID == "" || segs[0] != nodeID) {
			break
		}
		conns = append(conns, segs[pos+1])
		pos += 2
	}
	prefix = strings.Join(segs[:pos], "/")
	if pos > 0 {
		prefix += "/"
	}
	return conns, prefix, strings.Join(segs[pos:], "/")
}

// Walk follows the named connections from a node, answering the node they
// end on.
func (r Reach) Walk(from string, conns []string) (string, bool) {
	at := from
	for _, c := range conns {
		n, ok := r[at]
		if !ok {
			return "", false
		}
		next := ""
		for _, e := range n.Edges {
			if e.Conn == c {
				next = e.Lands
				break
			}
		}
		if next == "" {
			return "", false
		}
		at = next
	}
	return at, true
}

// Route is a shortest chain of connections from one node to another, of at
// most maxHops; the first declared connection wins a tie, so the answer is
// stable.
func (r Reach) Route(from, to string, maxHops int) ([]ReachEdge, bool) {
	type step struct {
		node string
		path []ReachEdge
	}
	seen := map[string]bool{from: true}
	queue := []step{{node: from}}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		if s.node == to {
			return s.path, true
		}
		if len(s.path) == maxHops {
			continue
		}
		n, ok := r[s.node]
		if !ok {
			continue
		}
		for _, e := range n.Edges {
			if seen[e.Lands] {
				continue
			}
			seen[e.Lands] = true
			queue = append(queue, step{node: e.Lands, path: append(append([]ReachEdge(nil), s.path...), e)})
		}
	}
	return nil, false
}

// Unreachable is Respell's refusal: the holder node has no chain of
// connections to the target node. Either is "" when the graph cannot place it.
type Unreachable struct {
	Holder, Target string
}

func (u *Unreachable) Error() string {
	return fmt.Sprintf("%s has no connection to %s", u.Holder, u.Target)
}

// Respell spells ref, an id in nodeID's frame, for the node that holds grid:
// an id in nodeID's frame that peels, hop by hop down grid's chain, to the
// holder's own route to the target.
func (r Reach) Respell(nodeID, grid, ref string, maxHops int) (string, error) {
	hconns, hprefix, _ := ChainOf(grid, nodeID)
	rconns, _, tail := ChainOf(ref, nodeID)
	holder, ok := r.Walk(nodeID, hconns)
	if !ok {
		return "", &Unreachable{}
	}
	target, ok := r.Walk(nodeID, rconns)
	if !ok {
		return "", &Unreachable{Holder: holder}
	}
	route, ok := r.Route(holder, target, maxHops)
	if !ok {
		return "", &Unreachable{Holder: holder, Target: target}
	}
	var b strings.Builder
	b.WriteString(hprefix)
	at := holder
	for _, e := range route {
		b.WriteString(at + "/" + e.Conn + "/")
		at = e.Lands
	}
	b.WriteString(tail)
	return b.String(), nil
}

// HeldAcross reports that a reference written into grid must be spelled for
// another node: grid is behind a connection of nodeID's and ref is not.
// Behind the same connection it is forwarded as sent and the node there
// decides; a grid of nodeID's own holds any id of nodeID's frame as it is.
func HeldAcross(grid, ref, nodeID string) bool {
	ns := OwnerNamespaceOf(grid, nodeID)
	if ns == "" || ns == UUIDOf(grid) {
		return false
	}
	if _, _, qualified := SplitID(ref); !qualified {
		return false
	}
	return !ChainedThrough(ref, ns)
}
