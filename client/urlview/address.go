package urlview

import (
	"net/url"
	"strings"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Address is where url tile t's view opens, page being its page
// (rpc.PageURL). A tile whose page the node serves carries no url_string, so
// the served page is read first: the row would open the empty string, a blank
// view and no error anywhere.
func Address(t *pb.Tile, page string) string {
	if rpc.PageContent(t) {
		return page
	}
	return t.UrlString
}

// Landed is the address a live view's location names: none inside the
// content API of the node page is on, since its path holds the token, and
// anywhere else the location itself.
func Landed(page, location string) (string, bool) {
	if contentAPI(location, page) {
		return "", false
	}
	return location, true
}

// contentAPI reports location in the content API of the node page is on.
func contentAPI(location, page string) bool {
	l, err := url.Parse(location)
	p, perr := url.Parse(page)
	return err == nil && perr == nil && l.Scheme == p.Scheme && l.Host == p.Host && strings.HasPrefix(l.Path, rpc.ContentPath)
}
