// Package traceevent spells the client's trace records: one constructor per
// thing the shim does, so what a dump can say is declared here rather than in
// syscall/js glue, and a closed set that grows — a navigation verb, a drop
// verdict, an event payload — cannot reach the trace unnamed.
package traceevent

import (
	"strings"

	"github.com/josephburnett/gridwell/client/errsurface"
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

// FlushFailed is a trace post the node did not keep. It is a record and never
// a notice: a notice is itself a record, and the two would feed each other.
func FlushFailed(reason string) Event {
	return Event{Src: "trace", Kind: "flush", Msg: "post failed: " + reason}
}
