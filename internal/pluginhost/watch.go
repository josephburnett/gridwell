package pluginhost

// The plugin's Watch stream is how a source that changes on its own reaches a
// grid open on screen: a moved listing becomes the GridChanged a write through
// this adapter would have published, and an entry changed in place the
// TileChanged that carries its bytes' news. Its scope is the contexts some
// client shows (SetInterest) and the contexts their link entries point into
// (noteLinks).

import (
	"context"
	"crypto/sha256"
	"io"
	"slices"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// Start builds the adapter and follows the plugin's Watch stream until stop,
// which waits for it. It is how a supervised plugin comes up; label names the
// plugin in the retry log.
func Start(cp pluginv1.PluginClient, mem *store.Namespace, sup Supervisor, label string) (a *Adapter, stop func()) {
	a = New(cp, mem, sup)
	return a, a.goListen(context.Background(), label, a.listen)
}

// listen is one stream per subprocess: a respawned plugin gets a fresh one the
// moment it is up rather than after a backoff spent dialing a process that is
// gone.
func (a *Adapter) listen(ctx context.Context, label string) {
	if a.sup == nil {
		a.listenProcess(ctx, label)
		return
	}
	var mu sync.Mutex
	epoch := 0
	kick := make(chan struct{}, 1)
	cancel := a.sup.OnHealth(func(bool, string) {
		mu.Lock()
		epoch++
		mu.Unlock()
		select {
		case kick <- struct{}{}:
		default:
		}
	})
	defer cancel()

	running, stop := -1, func() {}
	defer func() { stop() }()
	for {
		mu.Lock()
		e := epoch
		mu.Unlock()
		if e != running {
			stop()
			// A verdict was about the process that is gone; the next one
			// answers for itself.
			a.noteWatch("")
			running, stop = e, func() {}
			if healthy, _ := a.sup.Health(); healthy {
				stop = a.goListen(ctx, label, a.listenProcess)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-kick:
		}
	}
}

// goListen runs fn until the returned stop, which waits for it.
func (a *Adapter) goListen(ctx context.Context, label string, fn func(context.Context, string)) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(ctx, label)
	}()
	return func() { cancel(); <-done }
}

// listenProcess is one subprocess's stream, re-opened by namespace.Refollow.
// Only a process whose InfoResponse.watch declares it is asked; Unimplemented
// from one that does is a broken declaration, held as live updates off for the
// process's life. A transport failure re-opens quietly, the death being the
// supervisor's news; any other code is live updates off until a stream is
// open. Neither is darkness: the listings still answer (Adapter.liveOff).
//
// The stream is opened with the scope and only while it is not empty. Every
// open, first, after a drop, or for a moved scope, is the one path in
// followScope.
func (a *Adapter) listenProcess(ctx context.Context, label string) {
	namespace.Refollow{
		Label: label,
		Down:  func(string) {},
		Up:    func() { a.noteWatch("") },
		Attempt: func(ctx context.Context, established func()) error {
			ci, err := a.cp.Info(ctx, &pluginv1.InfoRequest{})
			if err != nil {
				return err
			}
			if !ci.Watch {
				<-ctx.Done()
				return nil
			}
			err = a.followScope(ctx, established)
			switch {
			case ctx.Err() != nil:
				return err
			case status.Code(err) == codes.Unimplemented:
				a.noteWatch("the plugin declares live updates but does not implement them: " + status.Convert(err).Message())
				<-ctx.Done()
				return nil
			case err != nil && !gwerr.IsTransport(err):
				a.noteWatch(status.Convert(err).Message())
			}
			return err
		},
	}.Run(ctx)
}

// followScope follows the scope until a stream ends, re-opening as it moves.
// A stream replaces the one before only once it is open, so a context in both
// scopes is watched throughout; each context the open adds is then checked
// (check), since a change between the client's listing and the open is
// otherwise announced by nothing. An attempt's first open adds its whole
// scope, which is what catches up after a drop. A context the scope dropped
// is shown by no one, so it is not checked.
func (a *Adapter) followScope(ctx context.Context, established func()) error {
	var cur *watchStream
	var watched []string
	defer func() { cur.stop() }()
	for {
		scope, moved := a.scopeNow()
		if len(scope) == 0 {
			cur.stop()
			cur, watched = nil, nil
			select {
			case <-ctx.Done():
				return nil
			case <-moved:
				continue
			}
		}
		next := a.openWatch(ctx, scope)
		select {
		case <-next.opened:
		case <-next.done:
			return next.err
		case <-moved:
			next.stop()
			continue
		case <-ctx.Done():
			next.stop()
			return nil
		}
		cur.stop()
		cur = next
		established()
		var added []string
		for _, c := range scope {
			if !slices.Contains(watched, c) {
				added = append(added, c)
			}
		}
		a.check(ctx, added)
		watched = scope
		close(cur.ack)
		select {
		case <-cur.done:
			return cur.err
		case <-moved:
		case <-ctx.Done():
			return nil
		}
	}
}

// check lists contexts together and announces, in one batch, those whose
// listing moved (settle).
func (a *Adapter) check(ctx context.Context, contexts []string) {
	var mu sync.Mutex
	var moved []string
	var wg sync.WaitGroup
	for _, c := range contexts {
		wg.Go(func() {
			if a.listingMoved(ctx, c) {
				mu.Lock()
				moved = append(moved, c)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	a.announce(moved)
}

// listingMoved lists context once and settles it. A context the source
// refuses to list right now has moved, because what a client holds of it is
// unknown. The listing runs through synthesize, so it writes rows as any read
// does.
func (a *Adapter) listingMoved(ctx context.Context, context string) bool {
	s, err := a.synthesize(ctx, rpc.EntryGridID(context))
	if ctx.Err() != nil {
		return false
	}
	return err != nil || a.settle(s, true)
}

// announce publishes each context's grid and every grid whose links read into
// one, each grid once however many of its links moved.
func (a *Adapter) announce(contexts []string) {
	var grids []string
	for _, c := range contexts {
		grids = append(grids, rpc.EntryGridID(c))
		for _, holder := range a.linkedFrom(c) {
			grids = append(grids, rpc.EntryGridID(holder))
		}
	}
	slices.Sort(grids)
	for _, g := range slices.Compact(grids) {
		a.publishGridChanged(g)
	}
}

// listingSum is a listing as a client holds it: every entry the source
// answered, its label, and whether the answer was authoritative. An entry's
// content stamp is not in it: bytes that moved are that entry's
// EntryChanged, and a directory whose files are written while its names stay
// has not moved.
type listingSum [sha256.Size]byte

// sumOf is the listing's sum. The zero sum is a dark listing, or one that
// does not marshal: noted when read, so the grid is announced when it lists
// again, and never noted by a check nobody read.
func sumOf(s *synthesized) listingSum {
	if s.dark {
		return listingSum{}
	}
	entries := make([]*pluginv1.Entry, len(s.entries))
	for i, e := range s.entries {
		entries[i] = proto.CloneOf(e)
		entries[i].ContentStamp = ""
	}
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(
		&pluginv1.ListResponse{Entries: entries, Authoritative: s.authoritative, SourceLabel: s.grid.SourceLabel})
	if err != nil {
		return listingSum{}
	}
	return sha256.Sum256(b)
}

// servedGrid is what clients may hold of one grid: each distinct listing
// read since it was last announced, and the last listing noted, with gen
// counting notes so a read can tell one landed while it was in flight.
type servedGrid struct {
	sums []listingSum
	last listingSum
	gen  uint64
}

func (a *Adapter) servedGen(gridID string) uint64 {
	a.servedMu.Lock()
	defer a.servedMu.Unlock()
	if g := a.served[gridID]; g != nil {
		return g.gen
	}
	return 0
}

// settle notes listing s and reports whether its grid must be announced, in
// one critical section so no read lands between check and note. A read moved
// only if another listing was noted while it was in flight; a check also if
// it differs from what was read. A grid nobody read is noted, not announced:
// a client re-reads what it shows itself when its stream reconnects.
func (a *Adapter) settle(s *synthesized, check bool) (moved bool) {
	sum := sumOf(s)
	a.servedMu.Lock()
	defer a.servedMu.Unlock()
	if a.served == nil {
		a.served = map[string]*servedGrid{}
	}
	g := a.served[s.grid.Id]
	if g == nil {
		g = &servedGrid{}
		a.served[s.grid.Id] = g
	}
	if check && s.dark && len(g.sums) == 0 {
		return false
	}
	raced := g.gen != s.since && g.last != sum
	differs := check && len(g.sums) > 0 && !slices.Equal(g.sums, []listingSum{sum})
	switch {
	case raced || differs:
		g.sums, moved = []listingSum{sum}, true
	case !slices.Contains(g.sums, sum):
		g.sums = append(g.sums, sum)
	}
	g.last = sum
	g.gen++
	return moved
}

// watchStream is one Watch stream followed on its own goroutine. Once open it
// waits for ack before reading, so what the open announces precedes its
// changes.
type watchStream struct {
	opened, ack, done chan struct{}
	err               error
	cancel            func()
}

func (a *Adapter) openWatch(ctx context.Context, scope []string) *watchStream {
	streamCtx, cancel := context.WithCancel(ctx)
	s := &watchStream{opened: make(chan struct{}), ack: make(chan struct{}), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(s.done)
		s.err = a.follow(streamCtx, ctx, scope, func() error {
			close(s.opened)
			select {
			case <-s.ack:
				return nil
			case <-streamCtx.Done():
				return streamCtx.Err()
			}
		})
	}()
	return s
}

// stop ends the stream and waits for its goroutine; nil is no stream.
func (s *watchStream) stop() {
	if s != nil {
		s.cancel()
		<-s.done
	}
}

// follow reads one Watch stream to its end. The stream is open once its header
// arrives, which the plugin sends on accepting it, or with its first change at
// the latest; a stream that ended unopened has no header, and Recv says how.
// A change is applied under apply, which outlives the stream, so a swap that
// ends the stream does not abandon a change it already took.
func (a *Adapter) follow(ctx, apply context.Context, scope []string, opened func() error) error {
	stream, err := a.cp.Watch(ctx, &pluginv1.WatchRequest{Contexts: scope})
	if err != nil {
		return err
	}
	if md, _ := stream.Header(); md != nil {
		if err := opened(); err != nil {
			return err
		}
	}
	for {
		ch, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		a.applyChange(apply, ch)
	}
}

// SetInterest takes this plugin's share of the node's interest, grids the
// node serves for it, as the contexts shown. A grid that no longer resolves
// is not watched. A share it cannot take leaves the scope where it was, so
// live updates are off for what is shown until one lands.
func (a *Adapter) SetInterest(_ context.Context, req *gridwellv1.SetInterestRequest) (*gridwellv1.SetInterestResponse, error) {
	var shown []string
	for _, gid := range req.GetGridIds() {
		_, c, err := a.resolveGrid(gid)
		switch status.Code(err) {
		case codes.OK:
		case codes.NotFound, codes.InvalidArgument:
			continue
		default:
			a.setSource(func() { a.interestOff = "what is shown could not be watched: " + err.Error() })
			return nil, err
		}
		if c != "" {
			shown = append(shown, c)
		}
	}
	a.setSource(func() { a.interestOff = "" })
	a.scopeMu.Lock()
	defer a.scopeMu.Unlock()
	a.shown = shown
	a.rescopeLocked()
	return &gridwellv1.SetInterestResponse{}, nil
}

// noteLinks records the contexts one live listing's link entries point into.
// A shown grid's links read their targets' content, so a change there is a
// change to what it shows: the targets' contexts join the scope.
func (a *Adapter) noteLinks(context string, entries []*pluginv1.Entry) {
	var into []string
	for _, e := range entries {
		if lt := e.GetLinkTarget(); lt != nil && lt.Context != context {
			into = append(into, lt.Context)
		}
	}
	slices.Sort(into)
	into = slices.Compact(into)
	a.scopeMu.Lock()
	defer a.scopeMu.Unlock()
	if slices.Equal(into, a.links[context]) {
		return
	}
	if a.links == nil {
		a.links = map[string][]string{}
	}
	if len(into) == 0 {
		delete(a.links, context)
	} else {
		a.links[context] = into
	}
	a.rescopeLocked()
}

// rescopeLocked derives the Watch scope, the contexts shown and those they
// link into, and closes moved when it changes. The caller holds scopeMu.
func (a *Adapter) rescopeLocked() {
	scope := slices.Clone(a.shown)
	for _, c := range a.shown {
		scope = append(scope, a.links[c]...)
	}
	slices.Sort(scope)
	scope = slices.Compact(scope)
	if !slices.Equal(scope, a.scope) {
		a.scope = scope
		close(a.moved)
		a.moved = make(chan struct{})
	}
}

// linkedFrom lists the contexts whose last live listing links into context.
func (a *Adapter) linkedFrom(context string) []string {
	a.scopeMu.Lock()
	defer a.scopeMu.Unlock()
	var out []string
	for holder, into := range a.links {
		if slices.Contains(into, context) {
			out = append(out, holder)
		}
	}
	slices.Sort(out)
	return out
}

// scopeNow is the scope and the channel that closes when it next moves.
func (a *Adapter) scopeNow() ([]string, <-chan struct{}) {
	a.scopeMu.Lock()
	defer a.scopeMu.Unlock()
	return slices.Clone(a.scope), a.moved
}

// applyChange announces a context whose listing moved (check) and an
// entry changed in place (applyEntry). A removal is announced as DeleteTile
// announces one: the refetch it causes runs the listing's own sweep, so the
// row retires by the one path that retires rows.
func (a *Adapter) applyChange(ctx context.Context, ch *pluginv1.Change) {
	switch p := ch.GetPayload().(type) {
	case *pluginv1.Change_ContextChanged:
		a.check(ctx, []string{p.ContextChanged.GetContext()})
	case *pluginv1.Change_EntryChanged:
		a.applyEntry(ctx, p.EntryChanged.GetContext(), p.EntryChanged.GetEntry())
	case *pluginv1.Change_EntryRemoved:
		// The one reading of the retired arm: a ContextChanged for its context.
		a.check(ctx, []string{p.EntryRemoved.GetContext()})
	}
}

// applyEntry tells every client that one entry changed in place: its row as
// GetGrid serves it, flagged content_changed, because a plugin row carries no
// version that could say its bytes moved. An entry the node will not accept
// or cannot find is the listing's to answer, so it is a ContextChanged.
func (a *Adapter) applyEntry(ctx context.Context, context string, e *pluginv1.Entry) {
	t, err := a.entryTile(ctx, context, e)
	if ctx.Err() != nil {
		return
	}
	if err != nil || t == nil {
		a.check(ctx, []string{context})
		return
	}
	a.hub.Publish(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
		TileChanged: &gridwellv1.TileChanged{Tile: t, ContentChanged: true},
	}})
}

// entryTile is e's wire tile in context, nil when the context holds none. A
// row keeps its own placement, so e alone builds it and refreshes its
// snapshot as a listing would, and retires a screenshot the change left behind
// (pageFaceStale); an untouched entry's placement flows from the whole listing
// (store.Namespace.Overlay), so the context is listed.
func (a *Adapter) entryTile(ctx context.Context, context string, e *pluginv1.Entry) (*gridwellv1.Tile, error) {
	if e == nil {
		return nil, nil
	}
	entries := []*pluginv1.Entry{e}
	if err := acceptEntries(context, entries); err != nil {
		return nil, err
	}
	gid, _, err := a.mem.LookupContext(context)
	if err != nil {
		return nil, err
	}
	_, minted, err := a.mem.LiveTileID(gid, e.Key)
	if err != nil {
		return nil, err
	}
	if !minted {
		s, err := a.synthesize(ctx, rpc.EntryGridID(context))
		if err != nil {
			return nil, err
		}
		return s.tileForKey(e.Key), nil
	}
	if err := a.mem.Refresh(gid, entries); err != nil {
		return nil, err
	}
	rows, err := a.mem.Overlay(gid, entries)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Key != e.Key {
			continue
		}
		if pageFaceStale(e, r.Tile) {
			if err := a.mem.DropURLPreview(r.ID); err != nil {
				return nil, err
			}
			r.PreviewBlobId = 0
		}
		tiles, err := buildTiles(rpc.EntryGridID(context), context, []store.ExtTile{r}, entries, a.mem.ContextKey)
		if err != nil {
			return nil, err
		}
		return tiles[0], nil
	}
	return nil, nil
}
