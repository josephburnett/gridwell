// Package events owns what the client does with one event off the Subscribe
// stream, beyond folding it into the cache. The shim applies the event and
// runs the plan; nothing here reads the DOM or the wire.
package events

import (
	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
)

// Plan is what one event asks for after cache.Apply. Empty strings ask for
// nothing.
type Plan struct {
	// DropPreviews names a removed tile whose decoded preview and rendered
	// raster must be released, or deleting tiles leaks browser images.
	DropPreviews string
	// ClearLatch and Fetch name a changed grid. GridChanged is the one
	// per-grid signal, so it also clears that grid's failure latch, and the
	// refetch is unconditional: the next descent would otherwise read stale.
	ClearLatch string
	Fetch      string
	// ClearContent names the body a changed tile shows, by rpc.ContentID, so
	// a read the server refused is asked once more: a target's bytes or a
	// link's target changed.
	ClearContent string
	// Health is a namespace's stream going dark or recovering; ReactHealth
	// says what to do about it.
	Health *pb.EventPluginHealth
}

// Route is the one table over the event kinds.
func Route(ev *pb.Event) Plan {
	switch p := ev.GetPayload().(type) {
	case *pb.Event_TileRemoved:
		return Plan{DropPreviews: p.TileRemoved.GetTileId()}
	case *pb.Event_TileChanged:
		if t := p.TileChanged.GetTile(); t != nil {
			return Plan{ClearContent: rpc.ContentID(t)}
		}
	case *pb.Event_GridChanged:
		id := p.GridChanged.GetGridId()
		return Plan{ClearLatch: id, Fetch: id}
	case *pb.Event_PluginHealth:
		return Plan{Health: p.PluginHealth}
	}
	return Plan{}
}

// HealthReaction is what a health transition costs the user. Both directions
// resync the source's grids: down changes what they are, and up means the
// fan-in resumed with no backlog, so this client missed that source's events
// too.
type HealthReaction struct {
	// Source is the errsurface key, one sticky notice per namespace.
	Source string
	// Resolve takes the notice down; Report puts it up with Detail.
	Resolve bool
	Report  bool
	// Resync names the source whose grids refetch, the health uuid itself.
	Resync string
}

// ReactHealth is the one table over a transition's direction.
func ReactHealth(h *pb.EventPluginHealth) HealthReaction {
	r := HealthReaction{Source: errsurface.PluginHealthSource(h.GetPluginUuid()), Resync: h.GetPluginUuid()}
	if h.GetHealthy() {
		r.Resolve = true
	} else {
		r.Report = true
	}
	return r
}
