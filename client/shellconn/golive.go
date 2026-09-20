package shellconn

// GoLivePlan is what the caller does with the row before the surface is
// placed.
type GoLivePlan struct {
	Unfreeze bool
	// FollowLink hands the gesture to the link's target, which owns the
	// address, the session, the history and the freeze writeback.
	FollowLink bool
}

// DecideGoLive answers what an explicit reconnect does to the row it descended
// into, for every kind that can go live; ok is false on a host that cannot
// place the surface, where the tile stays frozen and the caller says so its own
// way. Going live IS the unfreeze, so the standing freeze and a live surface
// never coexist, and the unfreeze fires only here, because DecideAutoLive never
// goes live over a standing freeze.
func DecideGoLive(canGoLive, frozen, leafLink bool) (plan GoLivePlan, ok bool) {
	if !canGoLive {
		return GoLivePlan{}, false
	}
	return GoLivePlan{Unfreeze: frozen, FollowLink: leafLink}, true
}
