package rpc

import (
	"errors"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// CheckRoute refuses a rule of server.yaml's routes (pb.Route) that matches
// nothing, or names both a plugin and outside, or neither.
func CheckRoute(r *pb.Route) error {
	switch {
	case r.Match == "":
		return errors.New("match is required: a route with no glob takes no address")
	case r.Plugin != "" && r.Outside:
		return errors.New("a route sends an address to a plugin or outside, not both")
	case r.Plugin == "" && !r.Outside:
		return errors.New("a route needs a plugin or outside: true; with neither it sends the address nowhere")
	}
	return nil
}

// Verdict is where an address goes: Plugin presents it, Outside opens it in
// the default browser, and the zero value is a plain url tile.
type Verdict struct {
	Plugin  string
	Outside bool
}

// Matcher reports whether a glob matches a whole address.
type Matcher func(pattern, address string) bool

// Decide is the first route whose glob matches address and, for a plugin's
// route, which that plugin presents (one of its patterns in presents, by
// plugin id, matches too). A plugin's route over an address it does not
// present falls through to the next.
func Decide(address string, routes []*pb.Route, presents map[string][]string, match Matcher) Verdict {
	for _, r := range routes {
		if !match(r.Match, address) {
			continue
		}
		if r.Outside {
			return Verdict{Outside: true}
		}
		for _, p := range presents[r.Plugin] {
			if match(p, address) {
				return Verdict{Plugin: r.Plugin}
			}
		}
	}
	return Verdict{}
}

// Routing is what a handshake gives the link funnel to ask Decide with: the
// node's routes and each plugin's presents, by its namespace.
func Routing(h *pb.HandshakeResponse) ([]*pb.Route, map[string][]string) {
	presents := map[string][]string{}
	for _, p := range h.GetPlugins() {
		if len(p.Presents) > 0 {
			presents[p.Uuid] = p.Presents
		}
	}
	return h.GetRoutes(), presents
}
