package rpc

import (
	"cmp"
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
// the default browser, and the zero value is a plain url tile. Broken is why
// Plugin cannot present it now, which loads nothing in its place.
type Verdict struct {
	Plugin  string
	Outside bool
	Broken  string
}

// Declared is what a plugin a route names declares: Presents from its
// answered Info, or Unknown, why the declaration is not known, its Info's
// refusal or the user's switch.
type Declared struct {
	Presents []string
	Unknown  string
}

// DeclaredBy is a plugin row's declaration, as both the node and the handshake
// read it: a switched-off plugin's is unknown, whatever it last declared.
func DeclaredBy(p *pb.PluginInfo) Declared {
	return Declared{Presents: p.GetPresents(), Unknown: cmp.Or(p.GetDisabled(), p.GetInfoError())}
}

// Matcher reports whether a glob matches a whole address.
type Matcher func(pattern, address string) bool

// Decide is the first route whose glob matches address and, for a plugin's
// route, which that plugin presents (one of its patterns in presents, by
// plugin id, matches too). A plugin's route over an address it does not
// present falls through to the next; one to a plugin whose declaration is
// unknown takes the address broken, since what it would present is unknown
// and the site is not its stand-in.
func Decide(address string, routes []*pb.Route, declared map[string]Declared, match Matcher) Verdict {
	for _, r := range routes {
		if !match(r.Match, address) {
			continue
		}
		if r.Outside {
			return Verdict{Outside: true}
		}
		d := declared[r.Plugin]
		if d.Unknown != "" {
			return Verdict{Plugin: r.Plugin, Broken: d.Unknown}
		}
		for _, p := range d.Presents {
			if match(p, address) {
				return Verdict{Plugin: r.Plugin}
			}
		}
	}
	return Verdict{}
}

// Routing is what a handshake gives the link funnel to ask Decide with: the
// node's routes and each plugin's declaration, by its namespace.
func Routing(h *pb.HandshakeResponse) ([]*pb.Route, map[string]Declared) {
	declared := map[string]Declared{}
	for _, p := range h.GetPlugins() {
		if d := DeclaredBy(p); len(d.Presents) > 0 || d.Unknown != "" {
			declared[p.Uuid] = d
		}
	}
	return h.GetRoutes(), declared
}
