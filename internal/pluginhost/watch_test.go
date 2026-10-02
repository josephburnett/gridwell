package pluginhost

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// watchPlugin declares Watch, unless undeclared, and answers call n (from 1)
// with serve, first sending the header, which says the stream is open, when
// accept says so. opening, when set, runs before the header.
type watchPlugin struct {
	oneEntryPlugin
	undeclared bool
	accept     func(n int32) bool
	opening    func(n int32)
	calls      atomic.Int32
	serve      func(n int32, ctx context.Context, send func(*pluginv1.Change) error) error
	// scopes, when set, hears each opened stream's contexts.
	scopes chan []string
}

func (p *watchPlugin) Info(ctx context.Context, req *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	resp, err := p.oneEntryPlugin.Info(ctx, req)
	if err != nil {
		return nil, err
	}
	resp.Watch = !p.undeclared
	return resp, nil
}

func (p *watchPlugin) Watch(req *pluginv1.WatchRequest, s grpc.ServerStreamingServer[pluginv1.Change]) error {
	n := p.calls.Add(1)
	if p.scopes != nil {
		p.scopes <- req.Contexts
	}
	if p.opening != nil {
		p.opening(n)
	}
	if p.accept != nil && p.accept(n) {
		if err := s.SendHeader(metadata.MD{}); err != nil {
			return err
		}
	}
	return p.serve(n, s.Context(), s.Send)
}

func contextChanged(c string) *pluginv1.Change {
	return &pluginv1.Change{Payload: &pluginv1.Change_ContextChanged{ContextChanged: &pluginv1.ContextChanged{Context: c}}}
}

func entryRemoved(c, key string) *pluginv1.Change {
	return &pluginv1.Change{Payload: &pluginv1.Change_EntryRemoved{EntryRemoved: &pluginv1.EntryRemoved{Context: c, Key: key}}}
}

// watching builds an adapter over p behind a real gRPC hop, attaches a
// subscriber, shows the "all" collection, and only then starts listening, as
// Start does, so nothing the plugin sends is published to nobody.
func watching(t *testing.T, p pluginv1.PluginServer, sup Supervisor) (*Adapter, <-chan *gridwellv1.Event) {
	t.Helper()
	a, seen := watchingNothing(t, p, sup)
	show(t, a, "all")
	return a, seen
}

// show makes the given contexts' grids the adapter's whole share of interest.
func show(t *testing.T, a *Adapter, contexts ...string) {
	t.Helper()
	var grids []string
	for _, c := range contexts {
		grids = append(grids, gridAddr(c))
	}
	if _, err := a.SetInterest(context.Background(), &gridwellv1.SetInterestRequest{GridIds: grids}); err != nil {
		t.Fatal(err)
	}
}

// watchingNothing is watching with nothing shown yet.
func watchingNothing(t *testing.T, p pluginv1.PluginServer, sup Supervisor) (*Adapter, <-chan *gridwellv1.Event) {
	t.Helper()
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp, closer, err := plugintest.Loopback(p)
	if err != nil {
		t.Fatal(err)
	}
	a := New(cp, memStore.Namespace("p1"), sup)
	ctx, cancel := context.WithCancel(context.Background())
	seen := make(chan *gridwellv1.Event, 64)
	go func() {
		_ = a.Subscribe(ctx, &gridwellv1.SubscribeRequest{}, func(ev *gridwellv1.Event) error {
			seen <- ev
			return nil
		})
	}()
	awaitSubscriber(t, a, seen)
	stop := a.goListen(ctx, "test watch", a.listen)
	t.Cleanup(func() {
		cancel()
		stop()
		closer()
	})
	return a, seen
}

// await returns the next event, failing after a bound generous enough for one
// Refollow backoff.
func await(t *testing.T, seen <-chan *gridwellv1.Event) *gridwellv1.Event {
	t.Helper()
	select {
	case ev := <-seen:
		return ev
	case <-time.After(10 * time.Second):
		t.Fatal("no event arrived")
		return nil
	}
}

// A Change is the event a write through the adapter would have published: the
// context's grid, under its derived address, whether or not the node has ever
// listed it. A removal is the same GridChanged DeleteTile publishes, whose
// refetch runs the listing's sweep. The open's own announcement of its scope
// comes first.
func TestWatchChangesArriveAsGridChanges(t *testing.T) {
	p := &watchPlugin{serve: func(_ int32, ctx context.Context, send func(*pluginv1.Change) error) error {
		if err := send(contextChanged("all")); err != nil {
			return err
		}
		if err := send(entryRemoved("never-listed", "k")); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}}
	_, seen := watching(t, p, nil)
	// The open's announcement and the change to "all" may coalesce in the hub.
	var ids []string
	for len(ids) == 0 || ids[len(ids)-1] != gridAddr("never-listed") {
		ids = append(ids, await(t, seen).GetGridChanged().GetGridId())
	}
	if all := ids[:len(ids)-1]; len(all) == 0 || len(all) > 2 || slices.ContainsFunc(all, func(id string) bool { return id != gridAddr("all") }) {
		t.Fatalf("events = %v, want GridChanged(%q) once or twice, then GridChanged(%q)", ids, gridAddr("all"), gridAddr("never-listed"))
	}
	if evs := collect(seen); len(evs) != 0 {
		t.Errorf("then %v, want nothing more", evs)
	}
}

// A plugin that does not declare Watch is healthy and never asked: the
// declaration is the owner, not an Unimplemented answer.
func TestWatchUndeclaredIsNeverAsked(t *testing.T) {
	p := &watchPlugin{undeclared: true, serve: func(int32, context.Context, func(*pluginv1.Change) error) error {
		return status.Error(codes.Unimplemented, "method Watch not implemented")
	}}
	a, seen := watching(t, p, nil)
	time.Sleep(1500 * time.Millisecond) // past the first Refollow backoff
	if n := p.calls.Load(); n != 0 {
		t.Errorf("Watch was opened %d times, want never", n)
	}
	if evs := collect(seen); len(evs) != 0 {
		t.Errorf("a plugin that does not watch announced %v", evs)
	}
	if s := a.source(); s.dark {
		t.Errorf("source dark (%q), want healthy", s.detail)
	}
}

// A plugin that declares Watch and answers Unimplemented contradicts its own
// handshake: its live updates are off, said once and not retried, because the
// process answers the same for its life. Its listings still answer, so the
// source is not dark.
func TestWatchDeclaredButUnimplementedTurnsLiveUpdatesOff(t *testing.T) {
	p := &watchPlugin{serve: func(int32, context.Context, func(*pluginv1.Change) error) error {
		return status.Error(codes.Unimplemented, "method Watch not implemented")
	}}
	a, seen := watching(t, p, nil)
	off := await(t, seen).GetPluginHealth()
	if off == nil || !off.Healthy || !strings.Contains(off.LiveUpdatesOff, "does not implement") {
		t.Fatalf("first event = %v, want healthy with live updates off naming the broken declaration", off)
	}
	time.Sleep(1500 * time.Millisecond) // past the first Refollow backoff
	if n := p.calls.Load(); n != 1 {
		t.Errorf("Watch was opened %d times, want once", n)
	}
	if evs := collect(seen); len(evs) != 0 {
		t.Errorf("then %v, want nothing more", evs)
	}
	if s := a.source(); s.dark {
		t.Errorf("source dark (%q), want a source that lists to stay light", s.detail)
	}
}

// A stream that ends is re-opened, quietly, and the re-open, like the first
// open, announces each context of its scope, so a client refetches what it
// shows and nothing sent while no stream was open is lost. A context with a row
// that no one shows is not announced.
func TestWatchEveryOpenAnnouncesItsScope(t *testing.T) {
	drop := make(chan struct{})
	p := &watchPlugin{
		accept: func(int32) bool { return true },
		serve: func(n int32, ctx context.Context, _ func(*pluginv1.Change) error) error {
			if n == 1 {
				select {
				case <-drop:
					return nil
				case <-ctx.Done():
					return nil
				}
			}
			<-ctx.Done()
			return nil
		},
	}
	a, seen := watching(t, p, nil)
	if _, err := a.mem.ContextID("inner"); err != nil {
		t.Fatal(err)
	}
	for _, what := range []string{"the first open", "the re-open"} {
		if ev := await(t, seen); ev.GetGridChanged().GetGridId() != gridAddr("all") {
			t.Fatalf("%s announced %v, want GridChanged(%q)", what, ev, gridAddr("all"))
		}
		if evs := collect(seen); len(evs) != 0 {
			t.Fatalf("then %v, want nothing more", evs)
		}
		if what == "the first open" {
			close(drop)
		}
	}
	if evs := collect(seen); len(evs) != 0 {
		t.Errorf("then %v, want nothing more", evs)
	}
}

// awaitScope returns the next opened stream's contexts.
func awaitScope(t *testing.T, scopes <-chan []string) []string {
	t.Helper()
	select {
	case s := <-scopes:
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("no stream was opened")
		return nil
	}
}

// A scope change loses no change: the old stream closes only once the new one
// is open, so a change to a context in both scopes during the swap arrives on
// a stream and the node announces nothing for it; a context the new scope adds
// is announced once, and one it drops not at all.
func TestWatchScopeChangeLosesNoChange(t *testing.T) {
	sends := make(chan func(*pluginv1.Change) error, 1)
	p := &watchPlugin{
		scopes: make(chan []string, 4),
		accept: func(int32) bool { return true },
		opening: func(n int32) {
			if n == 2 {
				// The source changes while the new stream is being opened.
				_ = (<-sends)(contextChanged("all"))
			}
		},
		serve: func(n int32, ctx context.Context, send func(*pluginv1.Change) error) error {
			if n == 1 {
				sends <- send
			}
			<-ctx.Done()
			return nil
		},
	}
	a, seen := watchingNothing(t, p, nil)
	for _, c := range []string{"inner", "gone", "more"} {
		if _, err := a.mem.ContextID(c); err != nil {
			t.Fatal(err)
		}
	}
	show(t, a, "all", "gone")
	if got := awaitScope(t, p.scopes); !slices.Equal(got, []string{"all", "gone"}) {
		t.Fatalf("first stream's scope = %v, want [all gone]", got)
	}
	for range 2 { // the first open's announcement, so the swap starts from an open stream
		await(t, seen)
	}
	swap := func(want map[string]int, contexts ...string) {
		t.Helper()
		show(t, a, contexts...)
		if got := awaitScope(t, p.scopes); !slices.Equal(got, contexts) {
			t.Fatalf("re-opened stream's scope = %v, want %v", got, contexts)
		}
		got := map[string]int{}
		for range want {
			got[await(t, seen).GetGridChanged().GetGridId()]++
		}
		for _, ev := range collect(seen) {
			got[ev.GetGridChanged().GetGridId()]++
		}
		if !maps.Equal(got, want) {
			t.Errorf("showing %v told the client %v, want %v", contexts, got, want)
		}
	}
	// The plugin's change to "all" during the swap, and "inner" added.
	swap(map[string]int{gridAddr("all"): 1, gridAddr("inner"): 1}, "all", "inner")
	// No change at all: only "more", which is added.
	swap(map[string]int{gridAddr("more"): 1}, "all", "inner", "more")
}

// While nothing of the plugin's is shown no stream is open: none before the
// first grid is shown, and the open one ends when the last one leaves.
func TestWatchNothingShownHoldsNoStream(t *testing.T) {
	ended := make(chan struct{}, 4)
	p := &watchPlugin{
		scopes: make(chan []string, 4),
		accept: func(int32) bool { return true },
		serve: func(_ int32, ctx context.Context, _ func(*pluginv1.Change) error) error {
			<-ctx.Done()
			ended <- struct{}{}
			return nil
		},
	}
	a, seen := watchingNothing(t, p, nil)
	time.Sleep(1500 * time.Millisecond) // past the first Refollow backoff
	if n := p.calls.Load(); n != 0 {
		t.Fatalf("Watch was opened %d times with nothing shown, want never", n)
	}
	show(t, a, "all")
	if got := awaitScope(t, p.scopes); !slices.Equal(got, []string{"all"}) {
		t.Fatalf("scope = %v, want [all]", got)
	}
	if ev := await(t, seen); ev.GetGridChanged().GetGridId() != gridAddr("all") {
		t.Fatalf("the open announced %v, want GridChanged(%q)", ev, gridAddr("all"))
	}
	show(t, a)
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream outlived the last grid shown")
	}
	time.Sleep(1500 * time.Millisecond)
	if n := p.calls.Load(); n != 1 {
		t.Errorf("Watch was opened %d times, want once", n)
	}
	if evs := collect(seen); len(evs) != 0 {
		t.Errorf("then announced %v, want nothing more", evs)
	}
}

// A coded refusal turns live updates off and leaves the source light: told to
// a subscriber already there and to one arriving during it, and cleared the
// moment a re-opened stream is accepted, with no change needed.
func TestWatchVerdictTurnsLiveUpdatesOff(t *testing.T) {
	p := &watchPlugin{
		accept: func(n int32) bool { return n > 1 },
		serve: func(n int32, ctx context.Context, _ func(*pluginv1.Change) error) error {
			if n == 1 {
				return status.Error(codes.ResourceExhausted, "the OS refused another change watch")
			}
			<-ctx.Done()
			return nil
		},
	}
	a, seen := watching(t, p, nil)
	off := await(t, seen).GetPluginHealth()
	if off == nil || !off.Healthy || !strings.Contains(off.LiveUpdatesOff, "refused another change watch") {
		t.Fatalf("first event = %v, want healthy with the refusal as live updates off", off)
	}
	if s := a.source(); s.dark {
		t.Errorf("source dark (%q), want a refused watch to leave it light", s.detail)
	}
	if got := a.source().liveOff; got != off.LiveUpdatesOff {
		t.Errorf("a subscriber arriving now is owed %q, want the refusal", got)
	}
	if on := await(t, seen).GetPluginHealth(); on == nil || !on.Healthy || on.LiveUpdatesOff != "" {
		t.Fatalf("second event = %v, want live updates back on", on)
	}
	if got := a.source().liveOff; got != "" {
		t.Errorf("live updates off (%q) once accepted, want on", got)
	}
}

// fakeSup is a Supervisor the test flips.
type fakeSup struct {
	mu      sync.Mutex
	healthy bool
	fns     map[int]func(bool, string)
	seq     int
}

func (s *fakeSup) Health() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.healthy, ""
}

func (s *fakeSup) OnHealth(fn func(bool, string)) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := s.seq
	s.fns[id] = fn
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.fns, id)
	}
}

func (s *fakeSup) set(healthy bool) {
	s.mu.Lock()
	s.healthy = healthy
	fns := make([]func(bool, string), 0, len(s.fns))
	for _, fn := range s.fns {
		fns = append(fns, fn)
	}
	s.mu.Unlock()
	for _, fn := range fns {
		fn(healthy, "")
	}
}

// One stream per process: the process going down ends its stream, and the
// next one gets a fresh stream the moment it is up, not after a backoff.
func TestWatchFollowsTheProcess(t *testing.T) {
	opened := make(chan int32, 4)
	ended := make(chan int32, 4)
	p := &watchPlugin{serve: func(n int32, ctx context.Context, _ func(*pluginv1.Change) error) error {
		opened <- n
		<-ctx.Done()
		ended <- n
		return nil
	}}
	sup := &fakeSup{healthy: true, fns: map[int]func(bool, string){}}
	watching(t, p, sup)
	wait := func(ch <-chan int32, want int32, what string) {
		t.Helper()
		select {
		case n := <-ch:
			if n != want {
				t.Fatalf("stream %d %s, want %d", n, what, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("stream %d never %s", want, what)
		}
	}
	wait(opened, 1, "opened")
	sup.set(false)
	wait(ended, 1, "ended")
	select {
	case n := <-opened:
		t.Fatalf("stream %d opened while the process is down", n)
	case <-time.After(200 * time.Millisecond):
	}
	sup.set(true)
	select {
	case n := <-opened:
		if n != 2 {
			t.Fatalf("stream %d opened, want 2", n)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("the respawned process got no stream inside half a second")
	}
}

// A refusal is the process's: a respawned process that refuses again, in the
// same words, is announced again, because the restart cleared what the old
// process said.
func TestARespawnedProcessRefusingAgainIsAnnouncedAgain(t *testing.T) {
	p := &watchPlugin{
		accept: func(int32) bool { return false },
		serve: func(int32, context.Context, func(*pluginv1.Change) error) error {
			return status.Error(codes.ResourceExhausted, "the OS refused another change watch")
		},
	}
	sup := &fakeSup{healthy: true, fns: map[int]func(bool, string){}}
	a, seen := watching(t, p, sup)
	first := await(t, seen).GetPluginHealth()
	if first == nil || !strings.Contains(first.LiveUpdatesOff, "refused") {
		t.Fatalf("first event = %v, want live updates off", first)
	}
	sup.set(false)
	time.Sleep(200 * time.Millisecond)
	if got := a.source().liveOff; got != "" {
		t.Fatalf("the old process's refusal (%q) outlived it", got)
	}
	sup.set(true)
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-seen:
			if hv := ev.GetPluginHealth(); hv != nil && strings.Contains(hv.LiveUpdatesOff, "refused") {
				return
			}
		case <-deadline:
			t.Fatal("the respawned process refused again and nobody was told")
		}
	}
}
