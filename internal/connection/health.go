package connection

// A connection's health is derived, never written: what the transport learns
// of a connection is a set of facts, each with one writer, and derive alone
// reads them, so no answer can overwrite another's news.

import (
	"maps"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/trace"
)

// reading is one writer's last word on whether the far node answered, why
// not, and when, by the transport's count; at 0 has said nothing.
type reading struct {
	ok     bool
	detail string
	at     uint64
}

// connFacts is what the transport knows of one connection, each fact with
// one writer: off (Disable), dialErr (ensureLive), stream (fanInRemote),
// landing and mismatch (learnRoot), liveOff (tellFar). Never persisted.
type connFacts struct {
	off      bool
	dialErr  string
	stream   reading
	landing  reading
	mismatch bool // the far end answered a home other than the stored links name
	liveOff  string
}

// connState is a connection's health as a client hears it.
type connState struct {
	up, told, mismatch bool
	// detail is why the connection's door does not open: why it is down, or
	// why its landing could not be learned while the far node answers.
	detail  string
	liveOff string // why the far node refused this node's share
}

// same is whether a client hears o as st: a new detail alone is no change,
// so a connection retrying every five seconds publishes once.
func (st connState) same(o connState) bool {
	return st.up == o.up && st.told == o.told && st.mismatch == o.mismatch && st.liveOff == o.liveOff
}

// derive is the one owner of a connection's health. A listening stream is
// the far node answering; otherwise the later of the stream's failure and the
// landing's Info decides, since an answer means not dark and a read left
// unanswered means dark. Changes are told while the stream has not failed and
// the far node took the share, whatever else answers.
func derive(f connFacts) connState {
	st := connState{up: true, told: f.liveOff == "" && (f.stream.ok || f.stream.at == 0), liveOff: f.liveOff}
	if f.landing.at > 0 && !f.landing.ok {
		st.detail = f.landing.detail
	}
	last := f.stream
	if f.landing.at > f.stream.at {
		last = f.landing
	}
	switch {
	case f.off:
		st.up, st.detail = false, rpc.DisabledDetail
	case f.dialErr != "":
		st.up, st.detail = false, f.dialErr
	case f.mismatch:
		st.up, st.detail, st.mismatch = false, f.landing.detail, true
	case f.stream.ok:
	case last.at > 0 && !last.ok:
		st.up, st.detail = false, last.detail
	}
	return st
}

// heard is how a connection reads before anything is known of it, which
// healthNow leaves out.
var heard = derive(connFacts{})

// setHealth applies one writer's edit to name's facts under mu, at the next
// count, and publishes the derived health when a client would hear it
// change (connState.same), answering whether it did.
func (s *Server) setHealth(name string, edit func(f *connFacts, at uint64)) bool {
	s.mu.Lock()
	s.count++
	f := s.facts[name]
	prev := derive(f)
	edit(&f, s.count)
	s.facts[name] = f
	st := derive(f)
	if !st.up {
		maps.DeleteFunc(s.farHealth, func(id string, _ *gridwellv1.Event) bool { return id == name || strings.HasPrefix(id, name+"/") })
	}
	s.mu.Unlock()
	if prev.same(st) {
		return false
	}
	msg := "up"
	switch {
	case !st.up:
		msg = "down: " + st.detail
	case st.liveOff != "":
		msg = "up, live updates off: " + st.liveOff
	case !st.told:
		msg = "up, not told"
	}
	trace.Emit("connection", "health", msg, map[string]string{"conn": name})
	s.hub.Publish(healthEvent(name, st))
	return true
}

func healthEvent(name string, st connState) *gridwellv1.Event {
	detail := st.detail
	if st.up {
		detail = ""
	}
	ev := rpc.HealthEvent(name, st.up, detail)
	ev.GetSourceHealth().LiveUpdatesOff = st.liveOff
	ev.GetSourceHealth().Told = proto.Bool(st.told)
	return ev
}

// stateOf is name's health as derive reads its facts now.
func (s *Server) stateOf(name string) connState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return derive(s.facts[name])
}

// healthNow is every connection whose health is not heard, in name order,
// then every far namespace's standing health.
func (s *Server) healthNow() []*gridwellv1.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*gridwellv1.Event
	for _, name := range slices.Sorted(maps.Keys(s.facts)) {
		if st := derive(s.facts[name]); !st.same(heard) {
			out = append(out, healthEvent(name, st))
		}
	}
	for _, id := range slices.Sorted(maps.Keys(s.farHealth)) {
		out = append(out, s.farHealth[id])
	}
	return out
}
