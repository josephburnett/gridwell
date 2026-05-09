package server

import (
	"encoding/json"
	"net/http"
)

// subscribe streams Subscribe events to the client as Server-Sent Events.
//
// SSE rather than gRPC-Web streaming because it is supported natively by the
// browser EventSource API and works through any HTTP/1.1 proxy. Each event
// is one JSON object on a `data: ...\n\n` line. The client demultiplexes by
// the event's `kind` field.
func (s *Server) subscribe(w http.ResponseWriter, r *http.Request) {
	uid, ok := userIDFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, cancel := s.store.SubscribeEvents(uid)
	defer cancel()

	enc := json.NewEncoder(w)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write([]byte("data: ")); err != nil {
				return
			}
			if err := enc.Encode(ev); err != nil {
				return
			}
			// Encoder appends '\n'; SSE needs another to terminate the event.
			if _, err := w.Write([]byte("\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
