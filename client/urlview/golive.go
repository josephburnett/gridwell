package urlview

// GoLive is what going live does with the descended row before a view is
// placed: clear the standing freeze, since going live is the unfreeze and the
// two facts never coexist, and follow a url link to its target, which owns
// the address, the session, the history and the freeze writeback.
type GoLive struct {
	Unfreeze   bool
	FollowLink bool
}

// DecideGoLive answers for a host that can place a view; ok is false on a
// host that cannot, where the tile stays frozen and the bar's open-in-tab
// slot is the descent instead. The unfreeze fires only on an explicit
// reconnect, because shellconn.DecideAutoLive never goes live over a standing
// freeze.
func DecideGoLive(liveURL, frozen, leafLink bool) (plan GoLive, ok bool) {
	if !liveURL {
		return GoLive{}, false
	}
	return GoLive{Unfreeze: frozen, FollowLink: leafLink}, true
}
