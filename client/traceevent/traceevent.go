// Package traceevent spells the client's trace records: one constructor per
// thing the shim does, so what a dump can say is declared here rather than in
// syscall/js glue, and a closed set that grows — a navigation verb, a drop
// verdict, an event payload — cannot reach the trace unnamed.
package traceevent

import (
	"strconv"
	"strings"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/nav"
)

// Event is one record's own fields; the ring stamps the rest.
type Event struct {
	Src, Kind, Msg string
	KV             map[string]string
}

// Ids go in KV, where a reader joins on them; everything else is prose in Msg.
func kv(pairs ...string) map[string]string {
	m := make(map[string]string, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			m[pairs[i]] = pairs[i+1]
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// Notice is a user-visible notice, under the source that raised it, so every
// notice the strip ever showed is in the trace by construction.
func Notice(sev errsurface.Severity, source, message string) Event {
	s := "error"
	if sev == errsurface.Info {
		s = "info"
	}
	return Event{Src: source, Kind: "notice", Msg: message, KV: kv("sev", s)}
}

// Log is a console line, under the tag it was written with.
func Log(tag, message string) Event {
	return Event{Src: strings.Trim(tag, "[]"), Kind: "log", Msg: message}
}

// The reasons a frame is asked for, each naming the site that asks. A paint
// that came from nowhere is the twitch, so the reason is the record.
const (
	WhyAnimation  = "ghost animation"
	WhyTransition = "pane transition"
	WhyGhost      = "ghost lerp"
	WhyTraceFade  = "ascent trace fade"
	WhyNotice     = "notice strip"
	WhyGridLoaded = "grid loaded"
	WhyContent    = "content loaded"
	WhyPreview    = "preview decoded"
	WhyDrag       = "drag snap"
)

// FrameScheduled and FrameDrawn bracket one animation frame, both under the
// reason it was asked for.
func FrameScheduled(why string) Event { return Event{Src: "frame", Kind: "schedule", Msg: why} }

func FrameDrawn(why string) Event { return Event{Src: "frame", Kind: "draw", Msg: why} }

// Framing is one settled viewport writeback. tileID is empty for a root grid,
// whose own row owns the framing.
func Framing(gridID, tileID string, cx, cy, zoom float64) Event {
	return Event{Src: "framing", Kind: "persist",
		Msg: "center " + f(cx) + "," + f(cy) + " zoom " + f(zoom),
		KV:  kv("grid", gridID, "tile", tileID)}
}

func f(v float64) string { return strconv.FormatFloat(v, 'g', 6, 64) }

// EventRecv and EventApplied bracket one Subscribe event: what arrived, and
// whether folding it in changed anything on screen.
func EventRecv(ev *pb.Event) Event {
	name, id := payloadOf(ev)
	return Event{Src: "events", Kind: "recv", Msg: name, KV: kv("id", id)}
}

func EventApplied(ev *pb.Event) Event {
	name, id := payloadOf(ev)
	return Event{Src: "events", Kind: "apply", Msg: name, KV: kv("id", id)}
}

// EventRefetch is the read an event asked for, which is where a stale view
// and a storm both show up.
func EventRefetch(gridID string) Event {
	return Event{Src: "events", Kind: "refetch", Msg: "grid", KV: kv("grid", gridID)}
}

// payloadOf names an event and the entity it is about. An unnamed arm is a
// payload the proto gained and this table did not.
func payloadOf(ev *pb.Event) (name, id string) {
	switch p := ev.GetPayload().(type) {
	case *pb.Event_TileChanged:
		return "tile changed", p.TileChanged.GetTile().GetId()
	case *pb.Event_TileRemoved:
		return "tile removed", p.TileRemoved.GetTileId()
	case *pb.Event_GridChanged:
		return "grid changed", p.GridChanged.GetGridId()
	case *pb.Event_PluginHealth:
		h := "namespace unhealthy"
		if p.PluginHealth.GetHealthy() {
			h = "namespace healthy"
		}
		return h, p.PluginHealth.GetPluginUuid()
	}
	return "unknown payload", ""
}

// OutboxPark is a write the server never answered, now owed; OutboxDrain is
// the kick that re-posts what is owed. Between the two is where the user's
// bytes wait out an outage.
func OutboxPark(op, id string) Event {
	return Event{Src: "outbox", Kind: "park", Msg: op, KV: kv("id", id)}
}

func OutboxDrain(n int) Event {
	return Event{Src: "outbox", Kind: "drain", Msg: strconv.Itoa(n) + " owed"}
}

// URLOpen, URLClose, ShellOpen, ShellClose and ShellExit are the live
// surfaces' state changes: a descent that went live, and what ended it.
func URLOpen(paneID, tileID string) Event {
	return Event{Src: "url", Kind: "open", Msg: "live view opens", KV: kv("pane", paneID, "tile", tileID)}
}

func URLClose(paneID, tileID string, freeze bool) Event {
	return Event{Src: "url", Kind: "close", Msg: closeMsg(freeze), KV: kv("pane", paneID, "tile", tileID)}
}

func ShellOpen(paneID, tileID string) Event {
	return Event{Src: "shell", Kind: "open", Msg: "stream opens", KV: kv("pane", paneID, "tile", tileID)}
}

func ShellClose(paneID, tileID string, freeze bool) Event {
	return Event{Src: "shell", Kind: "close", Msg: closeMsg(freeze), KV: kv("pane", paneID, "tile", tileID)}
}

// ShellExit is the far end going away, which is not this client closing the
// stream: sessionGone says the tmux session went with it.
func ShellExit(paneID, tileID, message string, sessionGone bool) Event {
	if sessionGone {
		message += " (session gone)"
	}
	return Event{Src: "shell", Kind: "exit", Msg: message, KV: kv("pane", paneID, "tile", tileID)}
}

// closeMsg: a frozen surface keeps its face and its address, a closed one is
// gone, and which happened is the whole question when a tile comes back blank.
func closeMsg(freeze bool) string {
	if freeze {
		return "frozen"
	}
	return "closed"
}

// TextSave is one document's bytes entering the save queue.
func TextSave(tileID, contentID string, n int) Event {
	return Event{Src: "text", Kind: "save", Msg: strconv.Itoa(n) + " bytes",
		KV: kv("tile", tileID, "content", contentID)}
}

// Nav is one navigation verb. Kind is the direction the frame stack moves, so
// a dump reads as descents and ascents whatever gesture asked for them.
func Nav(g nav.Gesture) Event {
	e := Event{Src: "nav"}
	door := g.Door.GetId()
	switch g.Kind {
	case nav.GestureDescend:
		e.Kind, e.Msg, e.KV = "push", "descend", kv("pane", g.PaneID, "door", door)
	case nav.GestureEnterLevel:
		e.Kind, e.Msg, e.KV = "push", "enter pane tile", kv("pane", g.PaneID, "door", door)
	case nav.GestureFollowLink:
		e.Kind, e.Msg, e.KV = "push", "follow link", kv("pane", g.PaneID, "door", door)
	case nav.GestureAscend:
		e.Kind, e.Msg, e.KV = "pop", "ascend "+strconv.Itoa(g.N), kv("pane", g.PaneID)
	case nav.GestureLeaveLevels:
		e.Kind, e.Msg = "pop", "leave "+strconv.Itoa(g.Count)+" levels"
	case nav.GestureLandLevel:
		e.Kind, e.Msg, e.KV = "pop", "land level", kv("pane", g.PaneID, "tile", g.TileID)
	case nav.GestureRestore:
		e.Kind, e.Msg, e.KV = "restore", "restore "+g.Raw, kv("pane", g.PaneID)
	case nav.GestureRestoreFromHistory:
		e.Kind, e.Msg = "restore", "restore session "+g.Raw
	case nav.GesturePromote:
		e.Kind, e.Msg, e.KV = "promote", "promote ephemeral", kv("pane", g.PaneID, "tile", g.OldID)
	case nav.GestureReEngage:
		e.Kind, e.Msg, e.KV = "reengage", "re-engage content", kv("pane", g.PaneID, "tile", g.TileID)
	default:
		e.Kind, e.Msg = "unknown", "unnamed gesture "+strconv.Itoa(int(g.Kind))
	}
	return e
}

// Focus is a real focus transfer; a press on the pane already focused is not
// one and records nothing.
func Focus(from, to string) Event {
	return Event{Src: "pane", Kind: "focus", Msg: "focus moves", KV: kv("from", from, "pane", to)}
}

// Drop is a committed release. The ghost's preview takes the same verdict and
// records nothing: that one is per pointer move.
func Drop(v dragdrop.DropAction, tileID, gridID string) Event {
	return Event{Src: "drag", Kind: "drop", Msg: dropName(v), KV: kv("tile", tileID, "grid", gridID)}
}

// dropName is the table over DecideDrop's verdicts.
func dropName(v dragdrop.DropAction) string {
	switch v {
	case dragdrop.DropNavigate:
		return "navigate"
	case dragdrop.DropNavigateSplit:
		return "navigate in a split"
	case dragdrop.DropFocusOnly:
		return "focus only"
	case dragdrop.DropCreateTemplate:
		return "create"
	case dragdrop.DropPanEnd:
		return "pan end"
	case dragdrop.DropDelete:
		return "delete"
	case dragdrop.DropRejected:
		return "rejected"
	case dragdrop.DropMove:
		return "move"
	case dragdrop.DropClone:
		return "clone"
	case dragdrop.DropLink:
		return "link"
	}
	return "unnamed verdict " + strconv.Itoa(int(v))
}

// FlushFailed is a trace post the node did not keep. It is a record and never
// a notice: a notice is itself a record, and the two would feed each other.
func FlushFailed(reason string) Event {
	return Event{Src: "trace", Kind: "flush", Msg: "post failed: " + reason}
}
