// Package events owns what the client does with one event off the Subscribe
// stream, beyond folding it into the cache. The shim applies the event and
// runs the plan; nothing here reads the DOM or the wire.
package events

import (
	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
)

// Plan is what one event asks for after cache.Apply. Empty strings and nil
// ask for nothing.
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
	// Revive names the namespace a changed grid belongs to. A key it answered
	// dead may be listed again, and a link is never rewritten to say so, so
	// its dead verdicts are asked once more.
	Revive string
	// Health is a namespace's health event; ReactHealth says what to do about
	// it.
	Health *pb.EventPluginHealth
	// Reframe is a root grid's new framing, applied to its doorways by
	// rpc.Reframe. It asks for no fetch: the listing did not change.
	Reframe *pb.GridFramingChanged
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
		return Plan{ClearLatch: id, Fetch: id, Revive: rpc.NamespaceOf(id)}
	case *pb.Event_GridFramingChanged:
		return Plan{Reframe: p.GridFramingChanged}
	case *pb.Event_PluginHealth:
		return Plan{Health: p.PluginHealth}
	}
	return Plan{}
}

// HealthReaction is what one health event costs the user. Its two notices
// follow their own fields at once. A move of the healthy bit, either way,
// resyncs the source's grids once the health holds (Resyncs): down changes
// what they are, and up means the fan-in resumed with no backlog. A refused
// Watch alone resyncs nothing, because the listings still answer.
type HealthReaction struct {
	Dark    StickyNotice
	LiveOff StickyNotice
	// Resync names the source whose grids refetch, "" for none.
	Resync string
}

// StickyNotice is one errsurface.Sticky notice: up with Message, or down when
// Message is empty.
type StickyNotice struct {
	Source  string
	Message string
}

// ReactHealth is the one table over a health event, given the source's label
// and whether the client held it dark before (cache.NoteHealth).
func ReactHealth(h *pb.EventPluginHealth, label string, wasDark bool) HealthReaction {
	uuid := h.GetPluginUuid()
	r := HealthReaction{
		Dark:    StickyNotice{Source: errsurface.PluginHealthSource(uuid)},
		LiveOff: StickyNotice{Source: errsurface.LiveUpdatesSource(uuid)},
	}
	if !h.GetHealthy() {
		r.Dark.Message = label + ": live updates stopped — " + h.GetDetail()
	}
	if off := h.GetLiveUpdatesOff(); off != "" {
		r.LiveOff.Message = label + ": live updates off — " + off
	}
	if wasDark == h.GetHealthy() {
		r.Resync = uuid
	}
	return r
}

// Owe names the grid whose read in flight is asked again after a tile event:
// the answer on the wire may predate the event, and installing it would drop
// the tile the cache folded in or could not fold. A grid nobody is reading
// owes nothing; the next read sees the source's state.
func Owe(ev *pb.Event) string {
	switch p := ev.GetPayload().(type) {
	case *pb.Event_TileChanged:
		return p.TileChanged.GetTile().GetGridId()
	case *pb.Event_TileRemoved:
		return p.TileRemoved.GetGridId()
	}
	return ""
}
