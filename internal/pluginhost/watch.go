package pluginhost

// The plugin's Watch stream is how a source that changes on its own reaches a
// grid open on screen: each Change becomes the event a write through this
// adapter would have published, so a client reacts to it exactly as to one.

import (
	"context"
	"io"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
// Unimplemented answers for the process's life, so the attempt holds and
// nothing is retried or said. A transport failure re-opens quietly, the
// process's death being the supervisor's news; any other code is the
// source's health until a change arrives.
func (a *Adapter) listenProcess(ctx context.Context, label string) {
	namespace.Refollow{
		Label: label,
		Down:  func(string) {},
		Up:    func() { a.noteWatch("") },
		Attempt: func(ctx context.Context, established func()) error {
			err := a.follow(ctx, established)
			switch {
			case ctx.Err() != nil:
				return err
			case status.Code(err) == codes.Unimplemented:
				<-ctx.Done()
				return nil
			case err != nil && !gwerr.IsTransport(err):
				a.noteWatch("live updates refused: " + err.Error())
			}
			return err
		},
	}.Run(ctx)
}

// follow reads one Watch stream to its end. A stream counts as established on
// its first change, the one moment the plugin has shown it is watching.
func (a *Adapter) follow(ctx context.Context, established func()) error {
	stream, err := a.cp.Watch(ctx, &pluginv1.WatchRequest{})
	if err != nil {
		return err
	}
	for {
		ch, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		established()
		a.applyChange(ch)
	}
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
