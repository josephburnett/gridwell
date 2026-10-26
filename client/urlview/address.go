package urlview

import (
	"net/url"
	"strings"

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

// Landed is the address a live view's location names: inside page, the
// tile's page ("" for a page its plugin serves), the site address it is the
// page of (rpc.PresentedSite), in the scheme of stored, the row's address;
// elsewhere in the node's content API, none, since its path holds the token;
// anywhere else, the location itself.
func Landed(page, stored, location string) (string, bool) {
	if page != "" {
		if strings.HasPrefix(location, page) {
			return rpc.PresentedSite(page, stored, location)
		}
		if contentAPI(location, page) {
			return "", false
		}
	}
	return location, true
}

// contentAPI reports location in the content API of the node page is on.
func contentAPI(location, page string) bool {
	l, err := url.Parse(location)
	p, perr := url.Parse(page)
	return err == nil && perr == nil && l.Scheme == p.Scheme && l.Host == p.Host && strings.HasPrefix(l.Path, rpc.ContentPath)
}
