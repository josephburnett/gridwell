// Package traceevent spells the client's trace records: one constructor per
// thing the shim does, so what a dump can say is declared here rather than in
// syscall/js glue.
package traceevent

import (
	"strconv"
	"strings"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/tracewire"
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

// Boot is the client's first record: the build it runs (see
// tracewire.BuildCommit) and the browser running it.
func Boot(commit, goVersion, userAgent string) Event {
	return Event{Src: "client", Kind: tracewire.KindBoot, Msg: "client boots",
		KV: kv("commit", commit, "go", goVersion, "ua", userAgent)}
}

// Unload is the page going away, a reload or a quit: beacons is how many
// writes it sent by beacon on the way out, which no rpc record shows.
func Unload(beacons int) Event {
	return Event{Src: "client", Kind: "unload", Msg: "page unloads",
		KV: kv("beacons", strconv.Itoa(beacons))}
}

// NodeBuild is the build the node's handshake named, beside the client's own
// on its boot record.
func NodeBuild(node string) Event {
	return Event{Src: "client", Kind: "handshake", Msg: "node runs " + shortOrUnstamped(node),
		KV: kv("node", node)}
}

// OtherBuild is the API refusing this page's build and what the page did
// about it (nodebuild.Decide).
func OtherBuild(client, node, verdict string) Event {
	return Event{Src: "client", Kind: "build", Msg: "the node runs " + shortOrUnstamped(node) +
		", this page " + shortOrUnstamped(client) + ": " + verdict,
		KV: kv("client", client, "node", node, "verdict", verdict)}
}

func shortOrUnstamped(build string) string {
	if build == "" {
		return "no build"
	}
	return tracewire.ShortCommit(build)
}

// Notice is a user-visible notice, under the source that raised it.
func Notice(sev errsurface.Severity, source, message string) Event {
	s := "error"
	if sev == errsurface.Info {
		s = "info"
	}
	return Event{Src: source, Kind: "notice", Msg: message, KV: kv("sev", s)}
}

// How a notice came down: its condition healed, the user pressed it away, or
// its deadline passed.
const (
	ClearResolve = "resolve"
	ClearDismiss = "dismiss"
	ClearExpire  = "expire"
)

// NoticeClear is a notice leaving the row, under the source that raised it, so
// a dump says how long the user saw it.
func NoticeClear(source, how string) Event {
	return Event{Src: source, Kind: "notice-clear", Msg: how, KV: kv("how", how)}
}

// Log is a console line, under the tag it was written with.
func Log(tag, message string) Event {
	return Event{Src: strings.Trim(tag, "[]"), Kind: "log", Msg: message}
}

// The reasons a draw is asked for, each naming the site that asks. A paint
// that came from nowhere is the twitch, so the reason is the record.
const (
	WhyAnimation  = "ghost animation"
	WhyTransition = "pane transition"
	WhyGhost      = "ghost lerp"
	WhyTraceFade  = "ascent trace fade"
	WhyNotice     = "notice row"
	WhyGridLoaded = "grid loaded"
	WhyContent    = "content loaded"
	WhyPreview    = "preview decoded"
	WhyDrag       = "drag snap"
	WhyReask      = "latched read re-asked"
	WhySelection  = "selection reconciled"
)

// DrawAsks is the paint asked for and not yet drawn. Asks coalesce, so the
// first reason is the draw's and the count says how many sites wanted it.
type DrawAsks struct {
	why string
	n   int
}

// Ask records one ask and reports whether it is the first, the one that must
// request the draw.
func (f *DrawAsks) Ask(why string) bool {
	f.n++
	if f.n > 1 {
		return false
	}
	f.why = why
	return true
}

// Take clears the asks, so an ask made during the draw arms the next draw.
func (f *DrawAsks) Take() DrawAsks {
	t := *f
	*f = DrawAsks{}
	return t
}

// Drawn is the one record of a draw.
func (f DrawAsks) Drawn(ms float64) Event {
	return Event{Src: "render", Kind: "draw", Msg: f.why,
		KV: kv("asks", strconv.Itoa(f.n), "ms", strconv.FormatFloat(ms, 'f', 1, 64))}
}

// Framing is one settled viewport writeback. tileID is empty for a root grid,
// whose own row owns the framing.
func Framing(gridID, tileID string, cx, cy, zoom float64) Event {
	return Event{Src: "framing", Kind: "persist",
		Msg: "center " + f(cx) + "," + f(cy) + " zoom " + f(zoom),
		KV:  kv("grid", gridID, "tile", tileID)}
}

func f(v float64) string { return strconv.FormatFloat(v, 'g', 6, 64) }

// EventsOpen is the event stream opening; resync says a gap came before it,
// so every held grid is read again (EventsResync).
func EventsOpen(resync bool) Event {
	msg := "stream opens"
	if resync {
		msg = "stream reopens after a gap"
	}
	return Event{Src: "events", Kind: "open", Msg: msg, KV: kv("resync", strconv.FormatBool(resync))}
}

// EventsEnd is an open stream ending: reason "clean" when the node closed it,
// "broken" with the error otherwise.
func EventsEnd(err error) Event {
	if err == nil {
		return Event{Src: "events", Kind: "end", Msg: "the node ended the stream", KV: kv("reason", "clean")}
	}
	return Event{Src: "events", Kind: "end", Msg: err.Error(), KV: kv("reason", "broken")}
}

// EventsResync is the re-read a gap owes: n grids asked again from source,
// "every" when the gap was the whole stream's.
func EventsResync(source string, n int) Event {
	if source == "" {
		source = "every"
	}
	return Event{Src: "events", Kind: "resync", Msg: strconv.Itoa(n) + " grids re-read",
		KV: kv("source", source, "n", strconv.Itoa(n))}
}

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

// EventRefetch is the read an event asked for.
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
	case *pb.Event_GridFramingChanged:
		return "grid framing changed", p.GridFramingChanged.GetGridId()
	case *pb.Event_SelectionChanged:
		return "selection changed", p.SelectionChanged.GetGridId()
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
// the kick that re-posts what is owed.
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

// URLReload is a live view loading its page again because its source changed
// it.
func URLReload(paneID, tileID string) Event {
	return Event{Src: "url", Kind: "reload", Msg: "live view reloads its changed page", KV: kv("pane", paneID, "tile", tileID)}
}

// URLMove is a live view handed to another pane with its page.
func URLMove(fromPaneID, toPaneID, tileID string) Event {
	return Event{Src: "url", Kind: "move", Msg: "live view moves",
		KV: kv("pane", toPaneID, "from", fromPaneID, "tile", tileID)}
}

// ShellMove is a live terminal handed to another pane with its socket.
func ShellMove(fromPaneID, toPaneID, tileID string) Event {
	return Event{Src: "shell", Kind: "move", Msg: "live terminal moves",
		KV: kv("pane", toPaneID, "from", fromPaneID, "tile", tileID)}
}

func ShellOpen(paneID, tileID string) Event {
	return Event{Src: "shell", Kind: "open", Msg: "stream opens", KV: kv("pane", paneID, "tile", tileID)}
}

func ShellClose(paneID, tileID string, freeze bool) Event {
	return Event{Src: "shell", Kind: "close", Msg: closeMsg(freeze), KV: kv("pane", paneID, "tile", tileID)}
}

// ShellExit is the far end going away, which is not this client closing the
// stream: tmuxGone says the tmux went with it.
func ShellExit(paneID, tileID, message string, tmuxGone bool) Event {
	if tmuxGone {
		message += " (tmux gone)"
	}
	return Event{Src: "shell", Kind: "exit", Msg: message, KV: kv("pane", paneID, "tile", tileID)}
}

// closeMsg: a frozen surface keeps its preview and its address, a closed one is
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

// Nav is one navigation verb; Kind is the direction the frame stack moves.
func Nav(g nav.Gesture) Event {
	e := Event{Src: "nav"}
	door := g.Door.GetId()
	switch g.Kind {
	case nav.GestureDescend:
		e.Kind, e.Msg, e.KV = "push", "descend", kv("pane", g.PaneID, "door", door)
	case nav.GestureEnterLevel:
		e.Kind, e.Msg, e.KV = "push", "enter workspace tile", kv("pane", g.PaneID, "door", door)
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
	case nav.GestureEngageLeaves:
		e.Kind, e.Msg = "reengage", "engage installed leaves"
	case nav.GestureReconcileLevel:
		e.Kind, e.Msg, e.KV = "restore", "reconcile workspace tile", kv("tile", g.TileID)
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

// PaneSplit, PaneClose, PaneSwap and PaneZoom are the pane tree's changes,
// client-only state no call shows: pane is the pane the change made or moved,
// from the pane it came from.
func PaneSplit(paneID, fromID string) Event {
	return Event{Src: "pane", Kind: "split", Msg: "pane opens", KV: kv("pane", paneID, "from", fromID)}
}

// PaneClose names every pane one crush closed.
func PaneClose(paneIDs []string) Event {
	return Event{Src: "pane", Kind: "close", Msg: strconv.Itoa(len(paneIDs)) + " closed",
		KV: kv("pane", strings.Join(paneIDs, " "))}
}

func PaneSwap(fromID, toID string) Event {
	return Event{Src: "pane", Kind: "swap", Msg: "panes swap", KV: kv("pane", toID, "from", fromID)}
}

func PaneZoom(paneID string, zoomed bool) Event {
	msg := "unzoom"
	if zoomed {
		msg = "zoom"
	}
	return Event{Src: "pane", Kind: "zoom", Msg: msg, KV: kv("pane", paneID)}
}

// DOMFocus is the document's keyboard focus landing on an element: which
// surface it is and the pane it serves, "" for none. Only a press, a user's
// descent or ascent, or a modal moves it, so one after the press that caused
// it is a steal.
func DOMFocus(element, paneID string) Event {
	return Event{Src: "dom", Kind: "dom-focus", Msg: element, KV: kv("pane", paneID)}
}

// Mods are the modifier keys held at a press.
type Mods struct{ Ctrl, Shift, Alt, Meta bool }

func (m Mods) String() string {
	var held []string
	for _, k := range []struct {
		on   bool
		name string
	}{{m.Ctrl, "ctrl"}, {m.Shift, "shift"}, {m.Alt, "alt"}, {m.Meta, "meta"}} {
		if k.on {
			held = append(held, k.name)
		}
	}
	return strings.Join(held, "+")
}

// Press is a mouse button going down, in the pane under it. forwarded says a
// live view's native layer heard it and relayed it, without the modifiers.
func Press(paneID string, button int, mods Mods, forwarded bool) Event {
	via := ""
	if forwarded {
		via = "live view"
	}
	return Event{Src: "gesture", Kind: "press", Msg: buttonName(button),
		KV: kv("pane", paneID, "mods", mods.String(), "via", via)}
}

// Release is a mouse button coming up. What it became is the drop record's.
func Release(paneID string, button int) Event {
	return Event{Src: "gesture", Kind: "release", Msg: buttonName(button), KV: kv("pane", paneID)}
}

// Cancel is Esc letting go of the drag armed in paneID; what it put back is
// gesture.Escape's.
func Cancel(paneID string, relayed bool) Event {
	via := ""
	if relayed {
		via = "live view"
	}
	return Event{Src: "gesture", Kind: "cancel", Msg: "escape", KV: kv("pane", paneID, "via", via)}
}

// Select is a selection gesture: how (toggle, band, clear), the pane, the grid
// and how many tiles the set holds after it.
func Select(how, paneID, gridID string, n int) Event {
	return Event{Src: "gesture", Kind: "select", Msg: how,
		KV: kv("pane", paneID, "grid", gridID, "n", strconv.Itoa(n))}
}

// buttonName is the table over MouseEvent.button.
func buttonName(b int) string {
	switch b {
	case 0:
		return "left"
	case 1:
		return "middle"
	case 2:
		return "right"
	}
	return "button " + strconv.Itoa(b)
}

// DropAt is where a committed release was made and landed: the pane the
// gesture was made in, the grabbed tile, the grid and cell it landed on (Cell
// says there is one), how many tiles it carried, and why a rejection was one
// (dragdrop.WhyRejected).
type DropAt struct {
	Pane, Tile, Grid string
	N                int
	X, Y             int64
	Cell             bool
	Why              string
}

// Drop is a committed release. The ghost's per-move preview records nothing.
func Drop(v dragdrop.DropAction, at DropAt) Event {
	e := Event{Src: "drag", Kind: "drop", Msg: dropName(v),
		KV: kv("pane", at.Pane, "tile", at.Tile, "grid", at.Grid, "why", at.Why)}
	if e.KV == nil {
		e.KV = map[string]string{}
	}
	if at.N > 0 {
		e.KV["n"] = strconv.Itoa(at.N)
	}
	if at.Cell {
		e.KV["x"] = strconv.FormatInt(at.X, 10)
		e.KV["y"] = strconv.FormatInt(at.Y, 10)
	}
	if len(e.KV) == 0 {
		e.KV = nil
	}
	return e
}

// dropName is the table over DecideDrop's verdicts.
func dropName(v dragdrop.DropAction) string {
	switch v {
	case dragdrop.DropNavigate:
		return "navigate"
	case dragdrop.DropNavigateSplit:
		return "navigate in a split"
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
	case dragdrop.DropCopy:
		return "copy"
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
