// Package golive owns what an explicit reconnect does to the row it descended
// into, for every kind that can go live. Going live IS the unfreeze, so the
// standing freeze and a live surface never coexist.
package golive

// Plan is what the caller does with the row before the surface is placed.
type Plan struct {
	Unfreeze bool
	// FollowLink hands the gesture to the link's target, which owns the
	// address, the session, the history and the freeze writeback.
	FollowLink bool
}

// Decide answers for a host that can place the surface; ok is false on a host
// that cannot, where the tile stays frozen and the caller says so its own way.
// The unfreeze fires only on an explicit reconnect, because
// shellconn.DecideAutoLive never goes live over a standing freeze.
func Decide(canGoLive, frozen, leafLink bool) (plan Plan, ok bool) {
	if !canGoLive {
		return Plan{}, false
	}
	return Plan{Unfreeze: frozen, FollowLink: leafLink}, true
}
