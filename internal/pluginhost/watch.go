package pluginhost

// The plugin's Watch stream is how a source that changes on its own reaches a
// grid open on screen: each Change becomes the event a write through this
// adapter would have published, so a client reacts to it exactly as to one.
// Its scope is the contexts some client shows (SetInterest).

import (
	"context"
	"io"
	"slices"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
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
// from one that does is a broken declaration, held as the source's health for
// the process's life. A transport failure re-opens quietly, the death being
// the supervisor's news; any other code is health until a stream is open.
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
				a.noteWatch("declares live updates but does not implement them: " + err.Error())
				<-ctx.Done()
				return nil
			case err != nil && !gwerr.IsTransport(err):
				a.noteWatch("live updates refused: " + err.Error())
			}
			return err
		},
	}.Run(ctx)
}

// followScope follows the scope until a stream ends, re-opening as it moves.
// A stream replaces the one before only once it is open, so a context in both
// scopes is watched throughout; each context the open adds is then announced,
// since a change between the client's listing and the open is otherwise
// announced by nothing. An attempt's first open adds its whole scope, which
// is what catches up after a drop. A context the scope dropped is shown by no
// one, so it is not announced.
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
		for _, c := range scope {
			if !slices.Contains(watched, c) {
				a.emitGridChanged(gridAddr(c))
			}
		}
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
// node serves for it, and makes their contexts the Watch stream's scope. A
// grid that no longer resolves is not watched.
func (a *Adapter) SetInterest(_ context.Context, req *gridwellv1.SetInterestRequest) (*gridwellv1.SetInterestResponse, error) {
	var scope []string
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
			scope = append(scope, c)
		}
	}
	slices.Sort(scope)
	scope = slices.Compact(scope)
	a.scopeMu.Lock()
	defer a.scopeMu.Unlock()
	if !slices.Equal(scope, a.scope) {
		a.scope = scope
		close(a.moved)
		a.moved = make(chan struct{})
	}
	return &gridwellv1.SetInterestResponse{}, nil
}

// scopeNow is the scope and the channel that closes when it next moves.
func (a *Adapter) scopeNow() ([]string, <-chan struct{}) {
	a.scopeMu.Lock()
	defer a.scopeMu.Unlock()
	return slices.Clone(a.scope), a.moved
}

// applyChange announces what a Change names as a GridChanged on the context's
// derived address, row or no row. A removal is announced the way DeleteTile
// announces one: the refetch it causes runs the listing's own sweep, so the
// row retires by the one path that retires rows.
func (a *Adapter) applyChange(ch *pluginv1.Change) {
	switch p := ch.GetPayload().(type) {
	case *pluginv1.Change_ContextChanged:
		a.emitGridChanged(gridAddr(p.ContextChanged.GetContext()))
	case *pluginv1.Change_EntryRemoved:
		a.emitGridChanged(gridAddr(p.EntryRemoved.GetContext()))
	}
}
