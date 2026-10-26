package urlview

import (
	"strings"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// View is what one live url view shows, as its row and its landings move it.
type View struct {
	// Page is the tile's page on the node (rpc.PageURL), "" for a page its
	// plugin serves.
	Page string
	// Site is the site address the view is at, and Shows the plugin whose
	// page shows it, "" for the site itself.
	Site, Shows string
	// Moving is the address a routed navigation sends the tile to, until a
	// row names it.
	Moving string
}

// Land is v after its page landed at location, and the site address that
// names (Landed), false for none. A page that landed is no longer moving.
// presenter is the routes' verdict on a site, for a view that came back into
// its page by its history.
func (v View) Land(stored, location string, presenter func(site string) string) (View, string, bool) {
	site, ok := Landed(v.Page, stored, location)
	if !ok {
		return v, "", false
	}
	v.Site, v.Moving = site, ""
	switch {
	case v.Page == "" || !strings.HasPrefix(location, v.Page):
		v.Shows = ""
	case v.Shows == "":
		v.Shows = presenter(site)
	}
	return v, site, true
}

// Follow is what v loads when its row reads t: the row's own (Address) when
// the row names the address v is moving to, or the address v is at with
// another taker than the one v shows, and v after; false to stay.
func (v View) Follow(t *pb.Tile) (View, string, bool) {
	if !rpc.WebContent(t) || rpc.PageContent(t) || t.UrlString == "" || v.Page == "" {
		return v, "", false
	}
	if v.Moving != "" && t.UrlString != v.Moving ||
		v.Moving == "" && (t.UrlString != v.Site || t.PresentedBy == v.Shows) {
		return v, "", false
	}
	return View{Page: v.Page, Site: t.UrlString, Shows: t.PresentedBy}, Address(t, v.Page), true
}
