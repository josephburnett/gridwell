package pluginhost

// The plugin's Watch stream is how a source that changes on its own reaches a
// grid open on screen: each Change becomes the event a write through this
// adapter would have published, so a client reacts to it exactly as to one.
// Its scope is the contexts some client shows (SetInterest) and the contexts
// their link entries point into (noteLinks).

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
// (checkAdded), since a change between the client's listing and the open is
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
		a.checkAdded(ctx, added)
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

// checkAdded lists each context once and announces its grid only when what
// the source answers now is not exactly what GetGrid served since the grid was
// last announced: the node checks, so a client refetches only what moved. A
// context the source cannot list right now, or that nobody read, is
// announced, because what a client holds is unknown. The listings run
// together and through synthesize, so they write rows as any read does.
func (a *Adapter) checkAdded(ctx context.Context, contexts []string) {
	var wg sync.WaitGroup
	for _, c := range contexts {
		wg.Go(func() {
			s, err := a.synthesize(ctx, rpc.EntryGridID(c))
			if ctx.Err() != nil {
				return
			}
			if err == nil && a.servedOnly(s) {
				return
			}
			a.emitGridChanged(rpc.EntryGridID(c))
		})
	}
	wg.Wait()
}

// listingSum is a listing as a client holds it: every entry the source
// answered, its label, and whether the answer was authoritative.
type listingSum [sha256.Size]byte

// sumOf is the listing's sum, false for a dark listing, which holds nothing
// to compare, or one that does not marshal, which is then never recorded and
// always announced.
func sumOf(s *synthesized) (listingSum, bool) {
	if s.dark {
		return listingSum{}, false
	}
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(
		&pluginv1.ListResponse{Entries: s.entries, Authoritative: s.authoritative, SourceLabel: s.grid.SourceLabel})
	if err != nil {
		return listingSum{}, false
	}
	return sha256.Sum256(b), true
}

// noteServed records a listing GetGrid answered.
func (a *Adapter) noteServed(s *synthesized) {
	sum, ok := sumOf(s)
	if !ok {
		return
	}
	a.servedMu.Lock()
	defer a.servedMu.Unlock()
	if a.served == nil {
		a.served = map[string][]listingSum{}
	}
	if !slices.Contains(a.served[s.grid.Id], sum) {
		a.served[s.grid.Id] = append(a.served[s.grid.Id], sum)
	}
}

// servedOnly reports whether s is the one listing served for its grid.
func (a *Adapter) servedOnly(s *synthesized) bool {
	sum, ok := sumOf(s)
	if !ok {
		return false
	}
	a.servedMu.Lock()
	defer a.servedMu.Unlock()
	return slices.Equal(a.served[s.grid.Id], []listingSum{sum})
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
	ctx, cancel := context.WithCancel(ctx)
	s := &watchStream{opened: make(chan struct{}), ack: make(chan struct{}), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(s.done)
		s.err = a.follow(ctx, scope, func() error {
			close(s.opened)
			select {
			case <-s.ack:
				return nil
			case <-ctx.Done():
				return ctx.Err()
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
func (a *Adapter) follow(ctx context.Context, scope []string, opened func() error) error {
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
		a.applyChange(ch)
	}
}

// SetInterest takes this plugin's share of the node's interest, grids the
// node serves for it, as the contexts shown. A grid that no longer resolves
// is not watched.
func (a *Adapter) SetInterest(_ context.Context, req *gridwellv1.SetInterestRequest) (*gridwellv1.SetInterestResponse, error) {
	var shown []string
	for _, gid := range req.GetGridIds() {
		_, c, err := a.resolveGrid(gid)
		switch status.Code(err) {
		case codes.OK:
		case codes.NotFound, codes.InvalidArgument:
			continue
		default:
			return nil, err
		}
		if c != "" {
			shown = append(shown, c)
		}
	}
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

// applyChange announces what a Change names as a GridChanged on the context's
// derived address, row or no row, and on every context that links into it,
// whose links read what changed. A removal is announced the way DeleteTile
// announces one: the refetch it causes runs the listing's own sweep, so the
// row retires by the one path that retires rows.
func (a *Adapter) applyChange(ch *pluginv1.Change) {
	var c string
	switch p := ch.GetPayload().(type) {
	case *pluginv1.Change_ContextChanged:
		c = p.ContextChanged.GetContext()
	case *pluginv1.Change_EntryRemoved:
		c = p.EntryRemoved.GetContext()
	default:
		return
	}
	a.emitGridChanged(rpc.EntryGridID(c))
	for _, holder := range a.linkedFrom(c) {
		a.emitGridChanged(rpc.EntryGridID(holder))
	}
}
