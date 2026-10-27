// Package pluginhost adapts a plugin.v1 plugin to the full Gridwell service,
// joining the plugin's content answers with its namespace of the node's store
// (ids, placement, framing): presentation verbs terminate here and content
// verbs pass through. Listing mints nothing; every entry answers under its
// derived address (rpc.EntryTileID) and a row appears only at Adapter.mint.
// The row is bookkeeping, resolved on the way in (Adapter.resolveTile) and
// never handed out, since a mint that renamed the entry would take the id out
// from under whoever stood on it; and GetTile is one List of the grid
// named, since a key→grid index would copy the plugin's structure. A dark
// source, or a listing the plugin answered from the cache, is published as this
// namespace's health and minted rows still read; a dark plugin fails the read,
// since a local subprocess has no cached answers to serve.
package pluginhost

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/eventhub"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/trace"
)

// Supervisor is whoever owns the plugin's subprocess. The adapter never decides
// liveness itself and keeps no state about it; nil still has a stream, just no
// health on it.
type Supervisor interface {
	// Health is the current state: up, or down with the reason.
	Health() (healthy bool, detail string)
	// OnHealth registers a listener for every transition until cancel runs.
	// The listener must not block.
	OnHealth(func(healthy bool, detail string)) (cancel func())
}

// Adapter implements namespace.Namespace over one plugin and its namespace of
// the node's store. The router calls it as a Go value; the one gRPC hop
// underneath is the plugin.v1 subprocess.
type Adapter struct {
	cp  pluginv1.PluginClient
	mem *store.Namespace
	sup Supervisor

	hub *eventhub.Hub[*gridwellv1.Event]

	// The source's state as the listings and the Watch stream found it, held
	// only to announce its transitions; setSource is the one writer. liveOff
	// is the Watch stream's failure and subscriptionOff SetView's, neither of
	// them darkness: a source that lists but cannot watch still answers live.
	srcMu           sync.Mutex
	srcDark         bool
	srcDetail       string
	liveOff         string
	subscriptionOff string
	// unanswered is why the process left the probe unanswered, "" once it
	// answers any call (heard). probing is the one probe in flight (recheck),
	// bounded by wait, which a verdict that reads the state hands over
	// (Answering): a plugin no verdict reads is never probed.
	unanswered string
	probing    atomic.Bool
	wait       atomic.Int64

	// shown is this plugin's share of the node's subscription, as grids, and
	// links is, per grid, the grids its last live listing links into.
	// The Watch stream's scope derives from both (scopeLocked); moved closes
	// when it changes. SetView writes shown, synthesize links and doors.
	scopeMu sync.Mutex
	shown   []string
	links   map[string][]string
	doors   map[string]entryRef
	scope   []string
	moved   chan struct{}

	// served is, per grid address, what a client may hold of it; settle is
	// its one check and note, and emitGridChanged forgets what it holds.
	servedMu sync.Mutex
	served   map[string]*servedGrid

	// told is, per entry, the content stamp it was last told under; fresh is
	// its one check and note.
	toldMu sync.Mutex
	told   map[entryRef]string

	lacks absentVerbs

	// decl is the plugin's last answered Info, or declErr its refusal, and
	// every reader reads it (info); a process coming up forgets the last one's.
	// Its watch derives told, so info writes it through setSource.
	declMu  sync.Mutex
	decl    *pluginv1.InfoResponse
	declErr error

	// moving keeps a listing from retiring the row a Move is rekeying: the
	// old grid lists the key no more, and the new one does not hold the row.
	moving sync.RWMutex
}

var _ namespace.Namespace = (*Adapter)(nil)

// New builds the adapter; the caller owns both halves' lifecycles, and the
// adapter listens to sup for as long as sup lives.
func New(cp pluginv1.PluginClient, mem *store.Namespace, sup Supervisor) *Adapter {
	a := &Adapter{cp: cp, mem: mem, sup: sup, hub: eventhub.New(rpc.EventKey), moved: make(chan struct{}), doors: map[string]entryRef{}}
	if sup != nil {
		sup.OnHealth(func(up bool, _ string) {
			a.lacks.forget()
			// A new process, or none, is the supervisor's news.
			a.srcMu.Lock()
			a.unanswered = ""
			a.srcMu.Unlock()
			if up {
				a.declMu.Lock()
				a.decl, a.declErr = nil, nil
				a.declMu.Unlock()
			}
		})
	}
	return a
}

// Info translates the plugin handshake, resolving each declared grid to
// a grid id and its persisted framing. It declares no root: a plugin is not a
// place.
func (a *Adapter) Info(ctx context.Context, _ *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	ci, err := a.info(ctx, false)
	if err != nil {
		return nil, err
	}
	resp := &gridwellv1.InfoResponse{DisplayName: ci.DisplayName, Glyph: ci.Glyph, Presents: ci.Presents}
	for _, m := range declaredGrids(ci) {
		out := &gridwellv1.PluginGrid{
			Id: m.Id, Label: m.Label, Glyph: m.Glyph,
		}
		if m.Grid != "" {
			out.GridId = rpc.EntryGridID(m.Grid)
			v, err := a.gridFraming(m.Grid)
			if err != nil {
				return nil, err
			}
			out.ViewCx, out.ViewCy, out.ViewZoom = v.Wire()
		}
		resp.PluginGrids = append(resp.PluginGrids, out)
	}
	return resp, nil
}

// info is the held declaration, asked for fresh or when none is held. A call
// its caller gave up, or one to a process that is not up, says nothing about
// the declaration, so the answer before it stands.
func (a *Adapter) info(ctx context.Context, fresh bool) (*pluginv1.InfoResponse, error) {
	a.declMu.Lock()
	ci, err := a.decl, a.declErr
	a.declMu.Unlock()
	if !fresh && (ci != nil || err != nil) {
		return ci, err
	}
	ci, err = a.cp.Info(ctx, &pluginv1.InfoRequest{})
	a.heard(ctx, err)
	if !gwerr.IsAbandoned(ctx, err) && a.processUp() {
		a.setSource(func() {
			a.declMu.Lock()
			a.decl, a.declErr = ci, err
			a.declMu.Unlock()
		})
	}
	return ci, err
}

// Answering is why the process does not answer now, "" while it does, read
// from what its calls last found and the supervisor, with nothing asked. A
// process not answering is probed (recheck), bounded by wait, so the verdict
// moves when it answers again.
func (a *Adapter) Answering(wait time.Duration) string {
	if a.sup != nil {
		if up, detail := a.sup.Health(); !up {
			return notAnswering(detail)
		}
	}
	a.wait.Store(int64(wait))
	why := a.source().unanswered
	if why != "" {
		a.recheck()
	}
	return why
}

// heard records a call's outcome: any outcome is the process answering, an
// Unavailable included, since a plugin says that of its source and a process
// that died is the supervisor's news. A call its caller gave up says
// nothing, so it is a reason to ask (recheck).
func (a *Adapter) heard(ctx context.Context, err error) {
	if gwerr.IsAbandoned(ctx, err) {
		a.recheck()
		return
	}
	a.setSource(func() { a.unanswered = "" })
}

// recheck asks the process for its Info once, unless a probe is in flight or
// no verdict has read this plugin; an Info unanswered by the bound is the
// process not answering.
func (a *Adapter) recheck() {
	wait := time.Duration(a.wait.Load())
	if wait == 0 || !a.probing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer a.probing.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		if _, err := a.info(ctx, true); err != nil && ctx.Err() != nil {
			a.setSource(func() { a.unanswered = notAnswering("no answer to Info in " + wait.String()) })
		}
	}()
}

func notAnswering(why string) string { return "plugin not responding: " + why }

// declaredGrids is the one place root_grid is still read: a plugin that
// declares no grids gets one derived from it, so an old binary keeps
// presenting; declared grids always win.
func declaredGrids(ci *pluginv1.InfoResponse) []*pluginv1.PluginGrid {
	if len(ci.Grids) > 0 || ci.RootGrid == "" {
		return ci.Grids
	}
	return []*pluginv1.PluginGrid{{Id: ci.RootGrid, Grid: ci.RootGrid}}
}

func (a *Adapter) Handshake(context.Context, *gridwellv1.HandshakeRequest) (*gridwellv1.HandshakeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "a plugin has no handshake of its own: the node enumerates the + menu, and a plugin's rows come from its Info")
}

func (a *Adapter) CreateTile(context.Context, *gridwellv1.CreateTileRequest) (*gridwellv1.TileResponse, error) {
	return nil, status.Error(codes.Unimplemented, "a plugin's grid takes no new tiles: a plugin serves its source's entries and creates nothing")
}

func (a *Adapter) CopyTile(context.Context, *gridwellv1.CopyTileRequest) (*gridwellv1.TileResponse, error) {
	return nil, status.Error(codes.Unimplemented, "a plugin's grid takes no new tiles, so a copy into one is refused")
}

func (a *Adapter) ShellTmuxAlive(context.Context, *gridwellv1.ShellTmuxAliveRequest) (*gridwellv1.ShellTmuxAliveResponse, error) {
	return nil, status.Error(codes.Unimplemented, "a plugin serves no shells: a tmux is the node's")
}

func (a *Adapter) OpenShell(context.Context, func() (*gridwellv1.OpenShellRequest, error), func(*gridwellv1.OpenShellResponse) error) error {
	return status.Error(codes.Unimplemented, "a plugin serves no shells: a tmux is the node's")
}

// gridFraming is the framing kept for one grid. An
// unreadable store is an error, not a silent none.
func (a *Adapter) gridFraming(gkey string) (rpc.View, error) {
	gid, ok, err := a.mem.LookupGrid(gkey)
	if err != nil || !ok {
		return rpc.View{}, err
	}
	return a.mem.RootFraming(gid)
}

// Subscribe serves this namespace's event stream: health of the subprocess and
// the source, GridChanged, GridFramingChanged and TileChanged. A subscriber
// arriving mid-outage, after a failure to tell, or to a source whose changes
// are not told, is told at once; a healthy, told plugin announces nothing.
func (a *Adapter) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, send func(*gridwellv1.Event) error) error {
	ch, detach := a.hub.Subscribe()
	defer detach()
	if err := send(nil); err != nil {
		return err
	}
	up := true
	if a.sup != nil {
		cancel := a.sup.OnHealth(func(healthy bool, detail string) { a.emitHealth(healthy, detail) })
		defer cancel()
		var detail string
		if up, detail = a.sup.Health(); !up {
			if err := send(rpc.HealthEvent("", false, detail)); err != nil {
				return err
			}
		}
	}
	if s := a.source(); up && s != (sourceState{}) {
		if err := send(s.event()); err != nil {
			return err
		}
	}
	return namespace.Relay(ctx, ch, send)
}

func (a *Adapter) emitHealth(healthy bool, detail string) {
	a.hub.Publish(rpc.HealthEvent("", healthy, detail))
}

// noteSource records what a listing found and announces the transition. A
// dead subprocess is the supervisor's news, so it is not recorded here.
func (a *Adapter) noteSource(dark bool, detail string) {
	if !a.processUp() {
		return
	}
	a.setSource(func() { a.srcDark, a.srcDetail = dark, detail })
}

// noteWatch records the Watch stream's verdict, "" for none, even while the
// process is down.
func (a *Adapter) noteWatch(liveOff string) {
	a.setSource(func() { a.liveOff = liveOff })
}

// setSource applies one edit and announces the transition, never while the
// process is down: that is the supervisor's news.
func (a *Adapter) setSource(edit func()) {
	a.srcMu.Lock()
	was := a.sourceLocked()
	edit()
	now := a.sourceLocked()
	a.srcMu.Unlock()
	if now != was && a.processUp() {
		a.trace("health", now.line(), map[string]string{
			"dark": strconv.FormatBool(now.dark), "liveOff": now.liveOff, "detail": now.detail, "told": strconv.FormatBool(!now.untold)})
		a.hub.Publish(now.event())
	}
}

// trace emits one pluginhost record naming this plugin.
func (a *Adapter) trace(kind, msg string, kv map[string]string) {
	if kv == nil {
		kv = map[string]string{}
	}
	kv["plugin"] = a.mem.ID()
	trace.Emit("pluginhost", kind, msg, kv)
}

func (a *Adapter) processUp() bool {
	if a.sup == nil {
		return true
	}
	healthy, _ := a.sup.Health()
	return healthy
}

// sourceState is the source as one health event tells it; the zero value is a
// light source whose changes are told, or whose declaration is not yet held.
type sourceState struct {
	dark, untold                bool
	detail, liveOff, unanswered string
}

func (s sourceState) line() string {
	msg := "source live"
	switch {
	case s.unanswered != "":
		msg = "process not answering"
	case s.dark:
		msg = "source dark"
	}
	if s.liveOff != "" {
		msg += ", live updates off"
	}
	return msg
}

func (s sourceState) event() *gridwellv1.Event {
	ev := rpc.HealthEvent("", !s.dark && s.unanswered == "", cmp.Or(s.unanswered, s.detail))
	ev.GetPluginHealth().LiveUpdatesOff = s.liveOff
	ev.GetPluginHealth().Told = proto.Bool(!s.untold)
	return ev
}

func (a *Adapter) source() sourceState {
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	return a.sourceLocked()
}

// sourceLocked is told while a held declaration of Watch has no failure since.
func (a *Adapter) sourceLocked() sourceState {
	s := sourceState{dark: a.srcDark, liveOff: cmp.Or(a.liveOff, a.subscriptionOff), unanswered: a.unanswered}
	if s.dark {
		s.detail = a.srcDetail
	}
	a.declMu.Lock()
	s.untold = s.liveOff != "" || a.decl != nil && !a.decl.Watch
	a.declMu.Unlock()
	return s
}

// sourceDetail is where the user is told the source, not the process, is out.
func sourceDetail(err error) string {
	if err == nil {
		return ""
	}
	return "the source is not answering: " + err.Error()
}

// emitGridChanged announces a grid a write moved under its derived address.
func (a *Adapter) emitGridChanged(gridID string) {
	if gridID == "" {
		return
	}
	a.servedMu.Lock()
	if g := a.served[gridID]; g != nil {
		g.sums = nil
	}
	a.servedMu.Unlock()
	a.publishGridChanged(gridID)
}

func (a *Adapter) publishGridChanged(gridID string) {
	a.hub.Publish(&gridwellv1.Event{Payload: &gridwellv1.Event_GridChanged{
		GridChanged: &gridwellv1.GridChanged{GridId: gridID},
	}})
}

// retiredPresentationRendered meant document only, with no way back to the
// source bytes. A plugin never hides those, so acceptEntries reads it as
// "both".
const retiredPresentationRendered = "rendered"

// acceptEntries is the one way a plugin's entries come in: it refuses a shape
// the node cannot present (serves_page off a url entry, an unknown
// text_presentation, a link it cannot draw) and maps a retired declaration
// onto the live vocabulary. grid is the grid listed.
func acceptEntries(gridKey string, entries []*pluginv1.Entry) error {
	for _, e := range entries {
		if lt := e.GetLinkTarget(); lt != nil {
			switch {
			case lt.Grid == "" || lt.Key == "":
				return status.Errorf(codes.InvalidArgument,
					"plugin: entry %q links to gridKey %q key %q; a link_target names both", e.Key, lt.Grid, lt.Key)
			case rpc.IsDoorKind(e.Kind):
				return status.Errorf(codes.InvalidArgument,
					"plugin: entry %q is a door with a link_target; a door opens its child_grid and has no link variant", e.Key)
			case lt.Grid == gridKey && lt.Key == e.Key:
				return status.Errorf(codes.InvalidArgument, "plugin: entry %q links to itself", e.Key)
			}
		}
		if e.ServesPage && rpc.KindOf(e.Kind).Content != rpc.ContentAddress {
			return status.Errorf(codes.InvalidArgument,
				"plugin: entry %q declares kind %q and serves_page; only a url entry serves a page, the node deriving its address — declare kind %q, or drop serves_page and serve a document body instead",
				e.Key, e.Kind, rpc.KindURL)
		}
		switch e.TextPresentation {
		case "", rpc.TextPresentationPlain, rpc.TextPresentationBoth:
		case retiredPresentationRendered:
			e.TextPresentation = rpc.TextPresentationBoth
		default:
			return status.Errorf(codes.InvalidArgument,
				"plugin: entry %q declares text_presentation %q; the vocabulary is %q, %q, or nothing at all",
				e.Key, e.TextPresentation, rpc.TextPresentationPlain, rpc.TextPresentationBoth)
		}
	}
	return nil
}

// buildTiles joins the overlay's rows with the listing's content facts, naming
// each by its derived address, a door's child grid included: the id a door
// hands the client must be the id GetGrid answers under.
func buildTiles(gridID, gridKey string, tiles []store.ExtTile, entries []*pluginv1.Entry, rowGrid func(int64) (string, error)) ([]*gridwellv1.Tile, error) {
	byKey := map[string]*pluginv1.Entry{}
	for _, e := range entries {
		byKey[e.Key] = e
	}
	out := make([]*gridwellv1.Tile, 0, len(tiles))
	for _, t := range tiles {
		pt := proto.Clone(t.Tile).(*gridwellv1.Tile)
		pt.Id = rpc.EntryTileID(gridKey, t.Key)
		pt.GridId = gridID
		pt.ChildGridId = ""
		e, listed := byKey[t.Key]
		switch {
		case listed && e.ChildGrid != "":
			pt.ChildGridId = rpc.EntryGridID(e.ChildGrid)
		case t.ChildGridID != 0:
			// A minted door the listing does not carry: its grid is read
			// back so the name handed out is still the address.
			gk, err := rowGrid(t.ChildGridID)
			if err != nil {
				return nil, err
			}
			pt.ChildGridId = rpc.EntryGridID(gk)
		}
		switch {
		case listed && pt.LinkTargetId != "":
			// A link's content facts are its target's, read through it.
			pt.StatusDetail = e.StatusDetail
		case listed:
			pt.ContentStamp = e.ContentStamp
			pt.ReadOnly = e.ReadOnly
			pt.ServesPage = e.ServesPage
			pt.TextPresentation = e.TextPresentation
			pt.PreviewBlobId = previewKey(pt.PreviewBlobId, e.PreviewStamp)
			pt.StatusDetail = e.StatusDetail
		}
		out = append(out, pt)
	}
	return out, nil
}

// previewKey is a plugin tile's preview key, exactly an internet tile's rule: the
// row's screenshot once one exists, until the next visit replaces it, and the
// plugin's own picture only before the first. The plugin's stamp is negated so
// the two number spaces never meet in the client's (tile, key) cache.
func previewKey(blobID, stamp int64) int64 {
	switch {
	case blobID != 0:
		return blobID
	case stamp > 0:
		return -stamp
	}
	return 0
}

// pagePreviewStale reports that row's screenshot no longer pictures entry e,
// whose source just said its bytes moved: the page is the plugin's, so the
// screenshot is of a page that is gone. The freeze gesture's screenshot is the
// user's and stays, and a link has no preview of its own.
func pagePreviewStale(e *pluginv1.Entry, row *gridwellv1.Tile) bool {
	return e.ServesPage && e.LinkTarget == nil && row.PreviewBlobId != 0 && !row.UrlFrozen
}

// synthesized is one grid as the adapter derives it; rows[i] matches tiles[i].
type synthesized struct {
	grid    *gridwellv1.Grid
	gridKey string
	gid     int64 // the grid's row, 0 when the grid is untouched
	rows    []store.ExtTile
	tiles   []*gridwellv1.Tile
	entries []*pluginv1.Entry
	// dark and authoritative are what the listing was; see absent.
	dark, authoritative, declared bool
	// since is the grid's servedGrid.gen before the listing was asked.
	since uint64
}

// resolveGrid reads a wire grid id as the grid it names plus the grid row
// backing it, 0 when nobody has touched the grid. Both shapes resolve.
func (a *Adapter) resolveGrid(gridID string) (gid int64, gridKey string, err error) {
	switch rpc.ShapeOf(gridID) {
	case rpc.ShapeRow:
		gid, _ = strconv.ParseInt(gridID, 10, 64)
		gridKey, err = a.mem.GridKey(gid)
		if errors.Is(err, store.ErrNotFound) {
			return 0, "", status.Errorf(codes.NotFound, "plugin: no grid %d", gid)
		}
		return gid, gridKey, err
	case rpc.ShapeKey:
		gridKey, _, isTile, _ := rpc.SplitEntryID(gridID)
		if isTile {
			return 0, "", status.Errorf(codes.InvalidArgument, "plugin: %q names a tile, not a grid", gridID)
		}
		gid, _, err = a.mem.LookupGrid(gridKey)
		return gid, gridKey, err
	default:
		return 0, "", status.Errorf(codes.InvalidArgument, "plugin: invalid grid_id %q", gridID)
	}
}

// synthesize is one grid with its join kept, so GetGrid, Find's key lookup
// and the mint's derived placement read the same tiles.
func (a *Adapter) synthesize(ctx context.Context, gridID string) (*synthesized, error) {
	a.moving.RLock()
	defer a.moving.RUnlock()
	gid, gkey, err := a.resolveGrid(gridID)
	if err != nil {
		return nil, err
	}
	since := a.servedGen(rpc.EntryGridID(gkey))
	// A transport failure is "not right now", not a verdict: nothing retires.
	// Whether the process or its source failed is the supervisor's fact.
	dark := false
	resp, err := a.cp.List(ctx, &pluginv1.ListRequest{Grid: gkey})
	a.heard(ctx, err)
	if err != nil {
		if gwerr.IsAbandoned(ctx, err) || !gwerr.IsTransport(err) || !a.processUp() {
			return nil, err
		}
		dark, resp = true, &pluginv1.ListResponse{}
	}
	outage := sourceDetail(err)
	// A cache answer serves and refreshes rows like a live one, but a cache
	// is never a verdict, whatever it claims: nothing retires, and its reason
	// is the source's health as a transport failure's is.
	cached := resp.Unreachable != ""
	if cached {
		outage, resp.Authoritative = resp.Unreachable, false
		a.trace("serve", "served from the plugin's cache", map[string]string{"grid": gridID, "cached": "1"})
	}
	a.noteSource(outage != "", outage)
	if err := acceptEntries(gkey, resp.Entries); err != nil {
		return nil, err
	}
	if !dark {
		a.noteLinks(gkey, resp.Entries)
	}
	listed := map[string]bool{}
	for _, e := range resp.Entries {
		listed[e.Key] = true
	}
	// An authoritative listing is a verdict on every key, so rows it does not
	// mention retire. An untouched entry has nothing to retire.
	if !dark && resp.Authoritative && gid != 0 {
		if err := a.mem.Sweep(gid, listed); err != nil {
			return nil, err
		}
	}
	if !dark && gid != 0 {
		if err := a.mem.Refresh(gid, resp.Entries); err != nil {
			return nil, err
		}
	}
	tiles, err := a.mem.Overlay(gid, resp.Entries)
	if err != nil {
		return nil, err
	}
	// A non-authoritative listing sweeps by arbitration: only a definitive
	// GONE from Probe retires an unlisted row.
	if !dark && !cached && !resp.Authoritative {
		kept := tiles[:0]
		for _, t := range tiles {
			if !listed[t.Key] {
				if pr, perr := a.probe(ctx, t.Key, gkey); perr == nil && pr.Presence == pluginv1.ProbeResponse_PRESENCE_GONE {
					if rerr := a.mem.Retire(t.ID); rerr != nil && !errors.Is(rerr, store.ErrNotFound) {
						return nil, rerr
					}
					continue // definitively gone: swept
				}
			}
			kept = append(kept, t) // listed, uncertain or alive: keep
		}
		tiles = kept
	}
	// host_content, glyph, writable and move ride the grid: a grid reached through
	// a door to another namespace has no local row. source_label is the listing's own, so it is
	// as fresh as the rows. A plugin creates no tiles.
	ci, err := a.info(ctx, false)
	if err != nil {
		return nil, err
	}
	addr := rpc.EntryGridID(gkey)
	selection, err := a.selection(gkey, gid)
	if err != nil {
		return nil, err
	}
	g := &gridwellv1.Grid{
		Id:           addr,
		AcceptsTiles: proto.Bool(false),
		Writable:     ci.Writable,
		HostContent:  ci.HostContent,
		Moves:        proto.Bool(ci.Move),
		Glyph:        ci.Glyph,
		SourceLabel:  resp.SourceLabel,
		Selection:    selection,
	}
	wire, err := buildTiles(addr, gkey, tiles, resp.Entries, a.mem.GridKey)
	if err != nil {
		return nil, err
	}
	declared := slices.ContainsFunc(declaredGrids(ci), func(m *pluginv1.PluginGrid) bool { return m.Grid == gkey })
	return &synthesized{grid: g, gridKey: gkey, gid: gid, rows: tiles, tiles: wire, entries: resp.Entries,
		dark: dark, authoritative: resp.Authoritative, declared: declared, since: since}, nil
}

func (a *Adapter) GetGrid(ctx context.Context, req *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	s, err := a.synthesize(ctx, req.GridId)
	if err != nil {
		return nil, err
	}
	if err := a.goneGrid(ctx, s); err != nil {
		return nil, err
	}
	if a.settle(s, false) {
		a.announce([]string{s.gridKey})
	}
	return &gridwellv1.GetGridResponse{Grid: s.grid, Tiles: s.tiles}, nil
}

// tileRef is a resolved tile; id is 0 when untouched.
type tileRef struct {
	id      int64
	gid     int64
	gridKey string
	key     string
}

// resolveTile reads either wire tile id shape, the one place both are read.
func (a *Adapter) resolveTile(tileID string) (tileRef, error) {
	switch rpc.ShapeOf(tileID) {
	case rpc.ShapeRow:
		id, _ := strconv.ParseInt(tileID, 10, 64)
		gid, key, tomb, err := a.mem.TileKey(id)
		// Ids are never reassigned, so a retired or unknown row is dead.
		if errors.Is(err, store.ErrNotFound) || tomb {
			return tileRef{}, gwerr.DeadLink("", "plugin: no tile %d", id)
		}
		if err != nil {
			return tileRef{}, err
		}
		gkey, err := a.mem.GridKey(gid)
		if err != nil {
			return tileRef{}, err
		}
		return tileRef{id: id, gid: gid, gridKey: gkey, key: key}, nil
	case rpc.ShapeKey:
		gkey, key, isTile, _ := rpc.SplitEntryID(tileID)
		if !isTile {
			return tileRef{}, status.Errorf(codes.InvalidArgument, "plugin: %q names a grid, not a tile", tileID)
		}
		gid, _, err := a.mem.LookupGrid(gkey)
		if err != nil {
			return tileRef{}, err
		}
		id, _, err := a.mem.LiveTileID(gid, key)
		if err != nil {
			return tileRef{}, err
		}
		return tileRef{id: id, gid: gid, gridKey: gkey, key: key}, nil
	default:
		return tileRef{}, status.Errorf(codes.InvalidArgument, "plugin: invalid tile_id %q", tileID)
	}
}

// mint is the one place a plugin tile becomes a row, at the placement GetGrid
// already answers, so minting never moves anything.
func (a *Adapter) mint(ctx context.Context, tileID string) (int64, error) {
	ref, err := a.resolveTile(tileID)
	if err != nil {
		return 0, err
	}
	if ref.id != 0 {
		return ref.id, nil
	}
	s, err := a.synthesize(ctx, rpc.EntryGridID(ref.gridKey))
	if err != nil {
		return 0, err
	}
	i, j := s.at(ref.key), slices.IndexFunc(s.entries, func(e *pluginv1.Entry) bool { return e.Key == ref.key })
	if i < 0 || j < 0 {
		return 0, status.Errorf(codes.NotFound, "plugin: no entry %q in gridKey %q", ref.key, ref.gridKey)
	}
	row, entry := s.rows[i], s.entries[j]
	gid, err := a.mem.GridOf(ref.gridKey)
	if err != nil {
		return 0, err
	}
	var child int64
	if entry.ChildGrid != "" {
		if child, err = a.mem.GridOf(entry.ChildGrid); err != nil {
			return 0, err
		}
	}
	return a.mem.Mint(gid, entry, child, row.X, row.Y, row.W, row.H)
}

// MintRef is the router's canonicalizer. A plugin's canonical id is its
// derived address, so this mints nothing; a row id (an older link) is
// tried as a tile row, then as a grid row.
func (a *Adapter) MintRef(_ context.Context, localID string) (string, error) {
	switch rpc.ShapeOf(localID) {
	case rpc.ShapeRow:
		if ref, err := a.resolveTile(localID); err == nil {
			return rpc.EntryTileID(ref.gridKey, ref.key), nil
		}
		if _, gkey, err := a.resolveGrid(localID); err == nil {
			return rpc.EntryGridID(gkey), nil
		}
		// A row nothing here answers to, retired or never this namespace's,
		// answers itself: what it names is not this call's verdict to make.
		return localID, nil
	case rpc.ShapeKey:
		return localID, nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "plugin: invalid id %q", localID)
	}
}

// tileByID resolves one tile through the same grid synthesis GetGrid uses.
func (a *Adapter) tileByID(ctx context.Context, tileID string) (*gridwellv1.Tile, error) {
	ref, err := a.resolveTile(tileID)
	if err != nil {
		return nil, err
	}
	s, err := a.synthesize(ctx, rpc.EntryGridID(ref.gridKey))
	if err != nil {
		return nil, err
	}
	if t := s.tileForKey(ref.key); t != nil {
		return t, nil
	}
	return nil, a.absent(ctx, s, ref.key, tileID)
}

// absent is the one answer for a key the listing does not carry: dead only
// when the source has said it is gone, by an authoritative listing or a
// definitive probe; a dark source is never dead.
func (a *Adapter) absent(ctx context.Context, s *synthesized, key, tileID string) error {
	if s.dark {
		return status.Errorf(codes.Unavailable, "plugin: source dark and %q not cached", tileID)
	}
	if s.authoritative {
		return gwerr.DeadLink("", "plugin: %q is gone", tileID)
	}
	pr, err := a.probe(ctx, key, s.gridKey)
	if err != nil {
		return err
	}
	if pr.Presence == pluginv1.ProbeResponse_PRESENCE_GONE {
		return gwerr.DeadLink("", "plugin: %q is gone", tileID)
	}
	return status.Errorf(codes.NotFound, "plugin: %q is not listed", tileID)
}

// goneGrid is absent for a grid its source does not declare: dead when its
// door is. One whose door no live listing has carried, or whose holder cannot
// be read, is unknown and reads as the grid it lists.
func (a *Adapter) goneGrid(ctx context.Context, s *synthesized) error {
	a.scopeMu.Lock()
	door, seen := a.doors[s.gridKey]
	a.scopeMu.Unlock()
	if s.dark || s.declared || !seen {
		return nil
	}
	holder, err := a.synthesize(ctx, rpc.EntryGridID(door.gridKey))
	if err == nil && holder.tileOpening(s.grid.Id) == nil && gwerr.IsDeadLink(a.absent(ctx, holder, door.key, door.key)) {
		return gwerr.DeadLink("", "plugin: grid %q is gone", s.gridKey)
	}
	return nil
}

// at is the index of key's row and tile, -1 when there is none.
func (s *synthesized) at(key string) int {
	return slices.IndexFunc(s.rows, func(r store.ExtTile) bool { return r.Key == key })
}

// tileForKey answers the wire tile for a plugin key, nil when there is none.
func (s *synthesized) tileForKey(key string) *gridwellv1.Tile {
	if i := s.at(key); i >= 0 {
		return s.tiles[i]
	}
	return nil
}

// tileOpening answers the door tile whose descent is the grid, or nil.
func (s *synthesized) tileOpening(childGridID string) *gridwellv1.Tile {
	if i := slices.IndexFunc(s.tiles, func(t *gridwellv1.Tile) bool { return t.ChildGridId == childGridID }); i >= 0 {
		return s.tiles[i]
	}
	return nil
}

func (a *Adapter) GetTile(ctx context.Context, req *gridwellv1.GetTileRequest) (*gridwellv1.TileResponse, error) {
	t, err := a.tileByID(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	return &gridwellv1.TileResponse{Tile: t}, nil
}

// MoveTile terminates at the store and is unversioned. It is a durable fact
// about an entry, so it mints the row it writes into.
func (a *Adapter) MoveTile(ctx context.Context, req *gridwellv1.MoveTileRequest) (*gridwellv1.TileResponse, error) {
	ref, err := a.resolveTile(req.TileId)
	if err != nil {
		return nil, err
	}
	toKey := ref.gridKey
	if req.GridId != "" {
		if _, toKey, err = a.resolveGrid(req.GridId); err != nil {
			return nil, err
		}
	}
	if toKey != ref.gridKey {
		ci, err := a.info(ctx, false)
		if err != nil {
			return nil, err
		}
		if !ci.Move {
			return nil, status.Error(codes.InvalidArgument, "plugin: the plugin does not move entries between its grids")
		}
	}
	id, err := a.mint(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	var changed bool
	if toKey == ref.gridKey {
		changed, err = a.mem.Move(id, 0, "", req.X, req.Y, req.W, req.H, req.Claim)
	} else {
		changed, err = a.moveAcross(ctx, req, ref, id, toKey)
	}
	return a.changedTile(ctx, id, changed, err, true)
}

// moveAcross is a move to another of the plugin's grids, which the plugin
// makes (plugin.proto Move) and the row follows under the key it answers. The
// claim is checked before the plugin is asked, since its move is not undone.
func (a *Adapter) moveAcross(ctx context.Context, req *gridwellv1.MoveTileRequest, ref tileRef, id int64, toKey string) (bool, error) {
	t, err := a.tileByID(ctx, req.TileId)
	if err != nil {
		return false, err
	}
	if err := store.ClaimPlacement(t, req.Claim, false); err != nil {
		return false, err
	}
	to, err := a.mem.GridOf(toKey)
	if err != nil {
		return false, err
	}
	a.moving.Lock()
	defer a.moving.Unlock()
	landed, err := ask(&a.lacks, "Move", func() (*pluginv1.MoveResponse, error) {
		return a.cp.Move(ctx, &pluginv1.MoveRequest{Key: ref.key, Grid: ref.gridKey, ToGrid: toKey})
	})
	if err != nil {
		return false, err
	}
	a.emitGridChanged(rpc.EntryGridID(ref.gridKey))
	return a.mem.Move(id, to, landed.Key, req.X, req.Y, req.W, req.H, req.Claim)
}

// SetTile terminates the framing, capture and rename arms at the store, one
// operation per call (rpc.OneOperation). A rename is the user's name on the
// node's row (store.Namespace.Rename) and the plugin is never told; a url
// writeback keeps only its screenshot (the title and trail are the plugin's).
func (a *Adapter) SetTile(ctx context.Context, req *gridwellv1.SetTileRequest) (*gridwellv1.TileResponse, error) {
	if why := rpc.OneOperation(req); why != "" {
		return nil, status.Error(codes.InvalidArgument, why)
	}
	if t := req.GetTile(); rpc.KindOf(t.GetKind()).Content == rpc.ContentAddress {
		if t.UrlString != "" || t.AltText != "" || t.UrlHistory != "" {
			return nil, status.Error(codes.InvalidArgument,
				"plugin: a url tile's title and history are its plugin's and its address is content; the writeback carries the screenshot alone")
		}
		if len(req.Preview) == 0 {
			return nil, status.Error(codes.InvalidArgument, "plugin: a url writeback with no screenshot writes nothing")
		}
	}
	var zoom rpc.ContentZoom
	if req.ContentZoom != nil {
		z, err := rpc.NewContentZoom(*req.ContentZoom)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		zoom = z
	}
	id, err := a.mint(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	var changed bool
	switch {
	case req.Rename != "":
		changed, err = a.mem.Rename(id, req.Version, req.Rename)
	case req.ContentZoom != nil:
		changed, err = a.mem.SetContentZoom(id, zoom)
	case req.UrlFrozen != nil:
		changed, err = a.mem.SetFrozen(id, *req.UrlFrozen, req.ClaimFrozen)
	default:
		t := req.GetTile()
		switch rpc.KindOf(t.GetKind()).Content {
		case rpc.ContentBody:
			changed, err = a.mem.SetTextScroll(id, t.GetTextX(), t.GetTextY(), t.GetTextW(), t.GetTextH(), t.GetTextMode())
		case rpc.ContentAddress:
			changed, err = a.mem.SetURLPreview(id, req.Preview)
		default:
			return nil, status.Errorf(codes.InvalidArgument, "plugin: unsupported SetTile kind %q", t.GetKind())
		}
	}
	return a.changedTile(ctx, id, changed, err, false)
}

// changedTile reads back a write to one tile row and, when it changed the
// row, announces it: a placement is part of its grid's answer, so a move
// announces the grid; any other write changes no listing, so it announces the
// tile and a client applies it in place. A write the store refused answers
// its error.
func (a *Adapter) changedTile(ctx context.Context, id int64, changed bool, err error, placed bool) (*gridwellv1.TileResponse, error) {
	if err != nil {
		return nil, gwerr.ToStatus(err)
	}
	resp, err := a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: strconv.FormatInt(id, 10)})
	if err != nil {
		return nil, err
	}
	if changed && placed {
		a.emitGridChanged(resp.GetTile().GetGridId())
	} else if changed {
		a.hub.Publish(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
			TileChanged: &gridwellv1.TileChanged{Tile: resp.GetTile()},
		}})
	}
	return resp, nil
}

// SetFraming persists framing on a door tile row or a plugin grid's row.
func (a *Adapter) SetFraming(ctx context.Context, req *gridwellv1.SetFramingRequest) (*gridwellv1.SetFramingResponse, error) {
	f, err := rpc.FramingOf(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if req.RootGridId != "" {
		_, gkey, err := a.resolveGrid(req.RootGridId)
		if err != nil {
			return nil, err
		}
		// Framing a root grid is a durable fact about it, so the grid gets its
		// row here.
		gid, err := a.mem.GridOf(gkey)
		if err != nil {
			return nil, err
		}
		changed, err := a.mem.SetFraming(0, gid, f)
		if err != nil {
			return nil, err
		}
		if changed {
			a.hub.Publish(rpc.FramingEvent(rpc.EntryGridID(gkey), f))
		}
		return &gridwellv1.SetFramingResponse{}, nil
	}
	id, err := a.mint(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	changed, err := a.mem.SetFraming(id, 0, f)
	if err != nil {
		return nil, err
	}
	resp, err := a.changedTile(ctx, id, changed, nil, false)
	if err != nil {
		return nil, err
	}
	return &gridwellv1.SetFramingResponse{Tile: resp.Tile}, nil
}

// SetSelection keeps a grid's selection on the node's row for its grid,
// as its framing is kept; the plugin never sees it. An added entry is
// touched, so it gets its row; one not in this grid is ignored, as home
// ignores a tile off its grid. A recursive clear walks the doors the node has
// rows for, the ones the user touched.
func (a *Adapter) SetSelection(ctx context.Context, req *gridwellv1.SetSelectionRequest) (*gridwellv1.SetSelectionResponse, error) {
	if why := rpc.SelectRefusal(req); why != "" {
		return nil, status.Error(codes.InvalidArgument, "plugin: "+why)
	}
	if req.All {
		changed, err := a.mem.ClearAllSelections()
		if err != nil {
			return nil, gwerr.ToStatus(err)
		}
		return &gridwellv1.SetSelectionResponse{}, a.announceSelections(changed)
	}
	_, gkey, err := a.resolveGrid(req.GridId)
	if err != nil {
		return nil, err
	}
	add := make([]int64, 0, len(req.Add))
	for _, id := range req.Add {
		ref, err := a.resolveTile(id)
		if gwerr.IsDeadLink(err) || err == nil && ref.gridKey != gkey {
			continue
		}
		if err != nil {
			return nil, err
		}
		row, err := a.mint(ctx, id)
		if status.Code(err) == codes.NotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		add = append(add, row)
	}
	remove := make([]int64, 0, len(req.Remove))
	for _, id := range req.Remove {
		ref, err := a.resolveTile(id)
		// The set names only live rows, so one without a row is not in it.
		if gwerr.IsDeadLink(err) || err == nil && ref.id == 0 {
			continue
		}
		if err != nil {
			return nil, err
		}
		remove = append(remove, ref.id)
	}
	gid, err := a.mem.GridOf(gkey)
	if err != nil {
		return nil, err
	}
	changed, err := a.mem.SetSelection(gid, store.SelectOp{Add: add, Remove: remove, Clear: req.Clear, Recurse: req.Recurse})
	if err != nil {
		return nil, gwerr.ToStatus(err)
	}
	return &gridwellv1.SetSelectionResponse{}, a.announceSelections(changed)
}

// announceSelections tells each grid's set, by grid key.
func (a *Adapter) announceSelections(grids []int64) error {
	for _, g := range grids {
		gk, err := a.mem.GridKey(g)
		if err != nil {
			return err
		}
		ids, err := a.selection(gk, g)
		if err != nil {
			return err
		}
		a.hub.Publish(rpc.SelectionEvent(rpc.EntryGridID(gk), ids))
	}
	return nil
}

// selection is a grid's selection on the wire, each row named by its key
// as every tile of the grid is; gid 0 is an untouched grid, which holds
// none.
func (a *Adapter) selection(gkey string, gid int64) ([]string, error) {
	if gid == 0 {
		return nil, nil
	}
	rows, err := a.mem.Selection(gid)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, id := range rows {
		_, key, _, err := a.mem.TileKey(id)
		if err != nil {
			return nil, err
		}
		out = append(out, rpc.EntryTileID(gkey, key))
	}
	return out, nil
}

// ReadContent answers a url's content as its address under its row's version,
// as home does (store.ReadContent); only a body is the plugin's.
func (a *Adapter) ReadContent(ctx context.Context, req *gridwellv1.ReadContentRequest, send func(*gridwellv1.ContentChunk) error) error {
	ref, err := a.resolveTile(req.TileId)
	if err != nil {
		return err
	}
	s, err := a.synthesize(ctx, rpc.EntryGridID(ref.gridKey))
	if err != nil {
		return err
	}
	if t := s.tileForKey(ref.key); t != nil && rpc.WebContent(t) {
		return send(&gridwellv1.ContentChunk{Data: []byte(t.UrlString), MediaType: rpc.AddressMediaType, Version: t.Version, ContentStamp: t.ContentStamp})
	}
	cs, err := a.cp.ReadContent(ctx, &pluginv1.ReadContentRequest{Key: ref.key})
	if err != nil {
		return err
	}
	// A plugin's stamp stands in for the version it lacks.
	return relay(cs.Recv, func(c *pluginv1.ContentChunk) *gridwellv1.ContentChunk {
		return &gridwellv1.ContentChunk{Data: c.Data, MediaType: c.MediaType, ContentStamp: c.ContentStamp}
	}, send)
}

// relay sends each chunk a plugin stream answers, as the wire's, until it ends.
func relay[P, W any](recv func() (P, error), wire func(P) W, send func(W) error) error {
	for {
		c, err := recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := send(wire(c)); err != nil {
			return err
		}
	}
}

// WriteContent writes a body through to the plugin by key, claiming the stamp
// it was read under; the version a plugin row lacks is not read. It commits
// only at the caller's clean close, answers the row as GetTile serves it
// carrying the written bytes' stamp, and tells it as the home tells its own
// writes, a TileChanged, flagged because the row's version cannot say so. A
// url's address is the node's row's, kept and latched as a rename is
// (store.Namespace.SetAddress), and the plugin is never told.
func (a *Adapter) WriteContent(ctx context.Context, recv func() (*gridwellv1.WriteContentRequest, error)) (*gridwellv1.TileResponse, error) {
	first, err := recv()
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "plugin: write: empty stream")
	}
	ref, err := a.resolveTile(first.TileId)
	if err != nil {
		return nil, err
	}
	// A row the listing cannot answer is the plugin's to refuse.
	if t, err := a.tileByID(ctx, first.TileId); err == nil && rpc.WebContent(t) {
		return a.writeAddress(ctx, first, recv)
	}
	wrote, err := ask(&a.lacks, "WriteContent", func() (*pluginv1.WriteContentResponse, error) {
		// Cancelled unless the stream closes cleanly, so the plugin never
		// sees the end of a write the caller broke off.
		wctx, cancel := context.WithCancel(ctx)
		defer cancel()
		up, err := a.cp.WriteContent(wctx)
		if err != nil {
			return nil, err
		}
		msg := &pluginv1.WriteContentRequest{Key: ref.key, Data: first.Data, ContentStamp: first.ContentStamp}
		for n := len(first.Data); ; {
			if err := up.Send(msg); err != nil {
				_, rerr := up.CloseAndRecv()
				return nil, cmp.Or(rerr, err)
			}
			next, rerr := recv()
			if errors.Is(rerr, io.EOF) {
				break
			}
			if rerr != nil {
				return nil, rerr
			}
			if n += len(next.Data); n > rpc.MaxContentBytes {
				return nil, status.Error(codes.InvalidArgument, "plugin: write: content too large")
			}
			msg = &pluginv1.WriteContentRequest{Data: next.Data}
		}
		return up.CloseAndRecv()
	})
	if err != nil {
		return nil, err
	}
	t, err := a.tileByID(ctx, first.TileId)
	if err != nil {
		return nil, err
	}
	t.ContentStamp = wrote.ContentStamp
	a.hub.Publish(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
		TileChanged: &gridwellv1.TileChanged{Tile: proto.CloneOf(t), ContentChanged: true},
	}})
	return &gridwellv1.TileResponse{Tile: t}, nil
}

func (a *Adapter) writeAddress(ctx context.Context, first *gridwellv1.WriteContentRequest, recv func() (*gridwellv1.WriteContentRequest, error)) (*gridwellv1.TileResponse, error) {
	data, err := namespace.Assembled(first, recv)
	if err != nil {
		return nil, err
	}
	id, err := a.mint(ctx, first.TileId)
	if err != nil {
		return nil, err
	}
	changed, err := a.mem.SetAddress(id, first.Version, string(data))
	return a.changedTile(ctx, id, changed, err, false)
}

func (a *Adapter) ServeContent(ctx context.Context, req *gridwellv1.ServeContentRequest, send func(*gridwellv1.ServeContentChunk) error) error {
	// No tile is an address the plugin presents (server.pageRoute): no key.
	var ref tileRef
	if req.TileId != "" {
		var err error
		if ref, err = a.resolveTile(req.TileId); err != nil {
			return err
		}
	}
	_, err := ask(&a.lacks, "ServeContent", func() (struct{}, error) {
		cs, err := a.cp.ServeContent(ctx, &pluginv1.ServeContentRequest{Key: ref.key, Subpath: req.Subpath,
			Method: req.Method, Query: req.Query, Body: req.Body,
			Headers: headers(req.Headers, func(n, v string) *pluginv1.Header { return &pluginv1.Header{Name: n, Value: v} })})
		if err == nil {
			err = relay(cs.Recv, func(c *pluginv1.ServeContentChunk) *gridwellv1.ServeContentChunk {
				return &gridwellv1.ServeContentChunk{Status: c.Status, MediaType: c.MediaType, Data: c.Data,
					Headers: headers(c.Headers, func(n, v string) *gridwellv1.Header { return &gridwellv1.Header{Name: n, Value: v} })}
			}, send)
		}
		a.heard(ctx, err)
		return struct{}{}, err
	})
	return err
}

// headers carries header lines across the two APIs' Header messages.
func headers[F interface {
	GetName() string
	GetValue() string
}, T any](from []F, to func(name, value string) T) []T {
	out := make([]T, 0, len(from))
	for _, h := range from {
		out = append(out, to(h.GetName(), h.GetValue()))
	}
	return out
}

// GetTilePreview answers the preview previewKey named.
func (a *Adapter) GetTilePreview(ctx context.Context, req *gridwellv1.GetTilePreviewRequest) (*gridwellv1.GetTilePreviewResponse, error) {
	ref, err := a.resolveTile(req.TileId)
	if err != nil {
		return nil, err
	}
	if ref.id != 0 {
		jpeg, err := a.mem.Preview(ref.id)
		if err != nil {
			return nil, gwerr.ToStatus(err)
		}
		if len(jpeg) > 0 {
			return &gridwellv1.GetTilePreviewResponse{Jpeg: jpeg}, nil
		}
	}
	resp, err := ask(&a.lacks, "GetPreview", func() (*pluginv1.GetPreviewResponse, error) {
		return a.cp.GetPreview(ctx, &pluginv1.GetPreviewRequest{Key: ref.key})
	})
	if err != nil {
		return nil, err
	}
	return &gridwellv1.GetTilePreviewResponse{Jpeg: resp.Jpeg}, nil
}

// probe is every Probe the adapter sends, the listing's sweep and the delete's
// arbitration included.
func (a *Adapter) probe(ctx context.Context, key, gridKey string) (*pluginv1.ProbeResponse, error) {
	return ask(&a.lacks, "Probe", func() (*pluginv1.ProbeResponse, error) {
		return a.cp.Probe(ctx, &pluginv1.ProbeRequest{Key: key, Grid: gridKey})
	})
}

func (a *Adapter) Probe(ctx context.Context, req *gridwellv1.ProbeRequest) (*gridwellv1.ProbeResponse, error) {
	// An id this namespace cannot read at all is GONE. A derived address
	// always resolves to a key, and the plugin says whether the key is there.
	ref, err := a.resolveTile(req.TileId)
	if err != nil {
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	resp, err := a.probe(ctx, ref.key, ref.gridKey)
	if err != nil {
		if gwerr.IsTransport(err) {
			return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_UNSPECIFIED}, nil
		}
		return nil, err
	}
	// The enums are defined identically. Map by name to keep that a checked
	// fact rather than a numeric coincidence; anything else is unspecified.
	return &gridwellv1.ProbeResponse{Presence: map[pluginv1.ProbeResponse_Presence]gridwellv1.ProbeResponse_Presence{
		pluginv1.ProbeResponse_PRESENCE_PRESENT: gridwellv1.ProbeResponse_PRESENCE_PRESENT,
		pluginv1.ProbeResponse_PRESENCE_GONE:    gridwellv1.ProbeResponse_PRESENCE_GONE,
	}[resp.Presence]}, nil
}

// DeleteTile hands the gesture to the plugin, which says what deleting means,
// and retires the row only on a definitive GONE: a delete may transform rather
// than remove, and retiring a live thing would kill every link to it.
func (a *Adapter) DeleteTile(ctx context.Context, req *gridwellv1.DeleteTileRequest) (*gridwellv1.DeleteTileResponse, error) {
	ref, err := a.resolveTile(req.TileId)
	if err != nil {
		return nil, err
	}
	if req.GetClaim() != nil {
		// The row as the client saw it, its grid fixed by its listing. A key
		// the source says is gone has nothing left the claim could lose, and
		// its delete is what retires the row.
		t, err := a.tileByID(ctx, req.TileId)
		switch {
		case gwerr.IsDeadLink(err):
		case err != nil:
			return nil, err
		default:
			if err := store.DeleteClaim(t, req, false); err != nil {
				return nil, gwerr.ToStatus(err)
			}
		}
	}
	if _, err := ask(&a.lacks, "Delete", func() (*pluginv1.DeleteResponse, error) {
		return a.cp.Delete(ctx, &pluginv1.DeleteRequest{Key: ref.key})
	}); err != nil {
		return nil, err
	}
	// Only a row can be retired. Deleting an untouched entry leaves no id to
	// retire, and the next listing simply does not name it.
	if ref.id != 0 {
		pr, perr := a.probe(ctx, ref.key, ref.gridKey)
		if perr == nil && pr.Presence == pluginv1.ProbeResponse_PRESENCE_GONE {
			if err := a.mem.Retire(ref.id); err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, fmt.Errorf("plugin: source deleted but row not retired: %w", err)
			}
		}
	}
	// Either way the source changed and the client must look again.
	a.emitGridChanged(rpc.EntryGridID(ref.gridKey))
	return &gridwellv1.DeleteTileResponse{}, nil
}
