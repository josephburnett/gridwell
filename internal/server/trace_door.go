package server

// The /trace door: the node's diagnostic ring, on the gated browser mux
// beside /shell. A client posts its own records as JSON lines and a dump
// writes the whole ring to <home>/dumps. Nothing here is a node fact — the
// ring answers no read, survives no restart, and dumping does not clear it.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/trace"
)

// traceDoor ingests JSON lines from the client. A malformed line is a 400
// naming its line number, and every good line before it is already in the
// ring: a sender that garbles one record still gets the rest of its gesture.
func (s *Server) traceDoor() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "trace: POST only", http.StatusMethodNotAllowed)
			return
		}
		var sent int64
		if h := r.Header.Get(tracewire.ClockHeader); h != "" {
			var err error
			if sent, err = strconv.ParseInt(h, 10, 64); err != nil {
				http.Error(w, "trace: "+tracewire.ClockHeader+": "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		if _, err := trace.Default().Ingest(r.Body, sent); err != nil {
			http.Error(w, "trace: "+err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// traceDumpDoor writes the ring out and answers where it went.
func (s *Server) traceDumpDoor() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "trace: POST only", http.StatusMethodNotAllowed)
			return
		}
		if s.cfg.Home == "" {
			http.Error(w, "trace: this server was built with no home to dump into", http.StatusInternalServerError)
			return
		}
		path, n, err := trace.Default().Dump(config.DumpsDir(s.cfg.Home), time.Now())
		if err != nil {
			http.Error(w, "trace: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(tracewire.DumpResponse{Path: path, Records: n}); err != nil {
			// The dump is already on disk; the reply is what broke.
			http.Error(w, "trace: "+err.Error(), http.StatusInternalServerError)
		}
	})
}
