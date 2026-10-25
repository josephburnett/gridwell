package rpc

import "errors"

// Route is one rule of server.yaml's ordered `routes`: Match is a glob over
// the whole address, and exactly one of Plugin (a plugin's id) and Outside is
// set. Its yaml keys are its field names lowercased, yaml.v3's default.
type Route struct {
	Match   string
	Plugin  string
	Outside bool
}

// Check refuses a rule that matches nothing, or names both a plugin and
// outside, or neither.
func (r Route) Check() error {
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
func Decide(address string, routes []Route, presents map[string][]string, match Matcher) Verdict {
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
