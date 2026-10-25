package urlview

import (
	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Address is where url tile t's view opens, page being its page
// (rpc.PageURL). A tile whose plugin serves its page carries no url_string,
// so the served page is read first: the row would open the empty string, a
// blank view and no error anywhere. A tile a plugin presents opens at that
// plugin's page for its address.
func Address(t *pb.Tile, page string) string {
	switch {
	case rpc.PageContent(t):
		return page
	case t.PresentedBy != "":
		return rpc.PresentedPageURL(page, t.UrlString)
	}
	return t.UrlString
}

// Landed is the address a live view's location names: the location itself,
// or for a presented tile, presented being its page ("" for any other), the
// site address inside it (rpc.PresentedSite), false outside it. stored is
// the row's address, whose scheme a presented location keeps.
func Landed(presented, stored, location string) (string, bool) {
	if presented == "" {
		return location, true
	}
	return rpc.PresentedSite(presented, stored, location)
}
