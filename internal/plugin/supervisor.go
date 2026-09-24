package plugin

import (
	"context"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/josephburnett/gridwell/api/compose"
	"github.com/josephburnett/gridwell/internal/trace"
)

// A plugin's subprocess is spawned, watched and respawned here. While the
// process is gone every call fails rather than being answered from a memory of
// what the plugin used to say. go-plugin offers no exit signal to select on,
// only Exited(), so the watch looks on a tick, and the respawn is backed off,
// capped and reset only after a process has proved it can stay up.

const (
	// watchInterval is the whole latency between a crash and the strip
	// saying so.
	watchInterval = 250 * time.Millisecond
	// firstBackoff is the pause before the first respawn, maxBackoff the cap.
	firstBackoff = 250 * time.Millisecond
	maxBackoff   = 30 * time.Second
	// stableFor is how long a process must run for its exit to count as a
	// one-off; sooner and the backoff keeps growing.
	stableFor = 30 * time.Second
)

// Supervisor owns one plugin's subprocess. It is a grpc.ClientConnInterface,
// so one plugin.v1 client is built over it for the plugin's life and every
// call routes to whichever process is up. Nothing upstream holds a handle to a
// dead one.
type Supervisor struct {
	uuid   string
	kind   string
	binary string
	cfg    map[string]string
	// stderr is the subprocess's stderr: the terminal, unchanged, and the
	// trace ring, which log.SetOutput cannot reach because a plugin's stderr
	// never passes through the node's log.
	stderr io.Writer

	mu   sync.Mutex
	proc *compose.Process
	// healthy and detail are the fact this type owns; detail is why the
	// plugin is down, carried to the user on the health event.
	healthy bool
	detail  string
	// spawnedAt is when proc started, for the crash-loop test.
	spawnedAt time.Time

	listenersMu sync.Mutex
	listeners   map[int]func(bool, string)
	listenerSeq int

	closeOnce sync.Once
	done      chan struct{}
	watchWG   sync.WaitGroup
}

// The supervisor is the connection every plugin.v1 call rides.
var _ grpc.ClientConnInterface = (*Supervisor)(nil)

// Supervise makes a spawn that fails at boot the caller's error, because a
// plugin the node cannot start must not come up as an empty grid, so nothing
// is watched until the first spawn succeeds.
func Supervise(uuid, kind, binary string, cfg map[string]string) (*Supervisor, error) {
	s := &Supervisor{
		uuid: uuid, kind: kind, binary: binary, cfg: cfg,
		stderr:    io.MultiWriter(os.Stderr, trace.PluginWriter(trace.Default(), uuid)),
		listeners: map[int]func(bool, string){},
		done:      make(chan struct{}),
	}
	if err := s.spawn(); err != nil {
		return nil, err
	}
	s.watchWG.Add(1)
	go s.watch()
	return s, nil
}

// Invoke routes to the process that is up right now.
func (s *Supervisor) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	conn, err := s.conn()
	if err != nil {
		return err
	}
	return conn.Invoke(ctx, method, args, reply, opts...)
}

func (s *Supervisor) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	conn, err := s.conn()
	if err != nil {
		return nil, err
	}
	return conn.NewStream(ctx, desc, method, opts...)
}

// conn's error code is Unavailable, so a caller that degrades on a transport
// failure degrades here.
func (s *Supervisor) conn() (grpc.ClientConnInterface, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc == nil {
		return nil, status.Errorf(codes.Unavailable, "plugin %s (%s) is down: %s", s.uuid, s.kind, s.detail)
	}
	return s.proc.Conn, nil
}

// Health is read by a subscriber before it listens, so a client that connects
// while the plugin is down learns that instead of waiting for a transition.
func (s *Supervisor) Health() (healthy bool, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.healthy, s.detail
}

// OnHealth's listener is called from the watch goroutine and must not block.
func (s *Supervisor) OnHealth(fn func(healthy bool, detail string)) (cancel func()) {
	s.listenersMu.Lock()
	defer s.listenersMu.Unlock()
	s.listenerSeq++
	id := s.listenerSeq
	s.listeners[id] = fn
	return func() {
		s.listenersMu.Lock()
		defer s.listenersMu.Unlock()
		delete(s.listeners, id)
	}
}

// Close is idempotent, and is the closer the registry runs at shutdown.
func (s *Supervisor) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.watchWG.Wait()
		s.mu.Lock()
		proc := s.proc
		s.proc = nil
		s.mu.Unlock()
		s.setHealth(false, "the node is shutting down")
		if proc != nil {
			proc.Kill()
		}
	})
}

func (s *Supervisor) spawn() error {
	proc, err := compose.LoadPlugin(s.binary, s.cfg, s.stderr)
	if err != nil {
		s.mu.Lock()
		s.proc = nil
		s.mu.Unlock()
		s.setHealth(false, "the plugin subprocess will not start: "+err.Error())
		return err
	}
	s.mu.Lock()
	s.proc, s.spawnedAt = proc, time.Now()
	s.mu.Unlock()
	s.setHealth(true, "")
	return nil
}

// watch is the supervisor's one goroutine.
func (s *Supervisor) watch() {
	defer s.watchWG.Done()
	backoff := firstBackoff
	for {
		if !s.sleep(watchInterval) {
			return
		}
		s.mu.Lock()
		proc, up := s.proc, time.Since(s.spawnedAt)
		s.mu.Unlock()
		if proc != nil && !proc.Exited() {
			continue
		}
		if proc != nil {
			backoff = respawnPause(backoff, up)
			// Read the id before the kill: go-plugin drops its reference to
			// the process there and ID() answers "" from then on.
			id := proc.ID()
			log.Printf("gridwell: plugin %s (%s) pid %s exited after %v; respawning in %v",
				s.uuid, s.kind, id, up.Round(time.Millisecond), backoff)
			s.mu.Lock()
			s.proc = nil
			s.mu.Unlock()
			proc.Kill() // reap go-plugin's own bookkeeping before replacing it
			// The pid rides the detail, so an operator can find the process
			// in a log once the strip says a plugin went down.
			s.setHealth(false, "the plugin subprocess (pid "+id+") exited")
		}
		if !s.sleep(backoff) {
			return
		}
		if err := s.spawn(); err != nil {
			// A spawn that will not start is a death at age zero.
			backoff = respawnPause(backoff, 0)
			log.Printf("gridwell: plugin %s (%s) respawn: %v (retrying in %v)", s.uuid, s.kind, err, backoff)
			continue
		}
		log.Printf("gridwell: plugin %s (%s) respawned", s.uuid, s.kind)
	}
}

// respawnPause takes the pause used before and how long the dead process ran.
// A process that proved it can stay up starts the pause over, so an ordinary
// crash comes back at once; one that died young doubles it, capped, so a
// binary that cannot stay up is retried forever without looping.
func respawnPause(last, ranFor time.Duration) time.Duration {
	if ranFor > stableFor {
		return firstBackoff
	}
	return min(last*2, maxBackoff)
}

// sleep waits d, or returns false the moment the supervisor closes.
func (s *Supervisor) sleep(d time.Duration) bool {
	select {
	case <-s.done:
		return false
	case <-time.After(d):
		return true
	}
}

// setHealth tells the listeners only when the state changed, never once per
// retry, so a flapping plugin does not spam the strip. The detail always takes
// the latest reason.
func (s *Supervisor) setHealth(healthy bool, detail string) {
	s.mu.Lock()
	changed := s.healthy != healthy
	s.healthy, s.detail = healthy, detail
	s.mu.Unlock()
	if !changed {
		return
	}
	s.listenersMu.Lock()
	fns := make([]func(bool, string), 0, len(s.listeners))
	for _, fn := range s.listeners {
		fns = append(fns, fn)
	}
	s.listenersMu.Unlock()
	for _, fn := range fns {
		fn(healthy, detail)
	}
}
