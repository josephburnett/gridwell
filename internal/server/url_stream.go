package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/josephburnett/gridwell/internal/store"
	"github.com/josephburnett/gridwell/internal/urldriver"
)

// urlStreamer is the subset of the driver the URLStream handler needs.
// Defined here as an interface so the server doesn't hard-depend on
// urldriver.Driver — any equivalent implementation could be slotted
// in.
type urlStreamer interface {
	Available() bool
	IsLive(userID, tileID int64) bool
	Subscribe(userID, tileID int64, sub urldriver.Subscriber) func()
	ForwardInput(userID, tileID int64, ev urldriver.InputEvent) error
}

// SetURLStreamer installs the streaming backend used by /rpc/URLStream.
// Without one, the endpoint returns 503.
func (s *Server) SetURLStreamer(d urlStreamer) {
	s.urlStreamer = d
}

// urlStreamMessage is one Client→Server JSON message, distinguished
// by Kind. Field shape follows urldriver.InputEvent.
type urlStreamMessage struct {
	Kind      string  `json:"kind"`
	X         float64 `json:"x,omitempty"`
	Y         float64 `json:"y,omitempty"`
	Button    string  `json:"button,omitempty"`
	DeltaY    float64 `json:"delta_y,omitempty"`
	Key       string  `json:"key,omitempty"`
	Code      string  `json:"code,omitempty"`
	Modifiers int64   `json:"modifiers,omitempty"`
	Width     int64   `json:"width,omitempty"`
	Height    int64   `json:"height,omitempty"`
}

// urlStreamServerMessage is one Server→Client text-frame JSON
// message. Binary frames carry raw JPEG bytes and have no JSON wrapper.
type urlStreamServerMessage struct {
	Kind string `json:"kind"`
	URL  string `json:"url,omitempty"`
}

// wsSubscriber is the urldriver.Subscriber the URLStream handler
// installs. It pushes frames and navigation events into bounded
// channels which the writer goroutine drains. Channels are buffered
// (frames=4, nav=8); on overflow, the oldest is dropped (frames) or
// the event is dropped (nav). Frame-drop is the right behavior for UI
// streaming — stale frames waste bandwidth more than they help.
type wsSubscriber struct {
	frames chan []byte
	nav    chan string

	closeOnce sync.Once
	done      chan struct{}
}

func newWSSubscriber() *wsSubscriber {
	return &wsSubscriber{
		frames: make(chan []byte, 4),
		nav:    make(chan string, 8),
		done:   make(chan struct{}),
	}
}

func (s *wsSubscriber) SendFrame(jpeg []byte) {
	for {
		select {
		case s.frames <- jpeg:
			return
		default:
			// Drop the oldest queued frame and retry.
			select {
			case <-s.frames:
			default:
				return
			}
		}
	}
}

func (s *wsSubscriber) SendNavigation(newURL string) {
	select {
	case s.nav <- newURL:
	default:
		// Drop on overflow.
	}
}

func (s *wsSubscriber) close() {
	s.closeOnce.Do(func() { close(s.done) })
}

// urlStream is the /rpc/URLStream WebSocket handler. Validates tile
// is a URL tile that is currently live and that the user has read
// permission, then bridges between the URLDriver's frame/nav stream
// and the client.
func (s *Server) urlStream(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.resolveSession(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.urlStreamer == nil || !s.urlStreamer.Available() {
		http.Error(w, "chromium unavailable", http.StatusServiceUnavailable)
		return
	}
	tileIDStr := r.URL.Query().Get("tile_id")
	tileID, err := strconv.ParseInt(tileIDStr, 10, 64)
	if err != nil || tileID <= 0 {
		http.Error(w, "missing or invalid tile_id", http.StatusBadRequest)
		return
	}
	tile, err := s.store.GetTile(r.Context(), uid, tileID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !tile.IsURL() {
		http.Error(w, "not a URL tile", http.StatusBadRequest)
		return
	}
	if !s.urlStreamer.IsLive(uid, tileID) {
		http.Error(w, "tile is not live", http.StatusConflict)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// gridwell client lives at the same origin in production; for
		// local-dev convenience accept any origin. M5 can lock this
		// down via the SecureCookie config.
		InsecureSkipVerify: true,
	})
	if err != nil {
		// Accept already wrote the error response.
		return
	}
	// Permanent context bound to the WS lifetime; cancel disconnects.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := newWSSubscriber()
	unsubscribe := s.urlStreamer.Subscribe(uid, tileID, sub)
	defer unsubscribe()
	defer sub.close()

	// Writer goroutine: drain frames + nav into the WS.
	//
	// Write timeout is generous (30s) because the client's JS event
	// loop is single-threaded — a slow page navigation can wedge it
	// long enough that the browser's WS receive buffer fills and our
	// TCP send blocks. A short timeout would tear the WS down on
	// transient backpressure; the user would experience "clicks
	// stopped working" as the reconnected WS sat in CONNECTING. The
	// frames channel already has bounded buffering with oldest-drop
	// (wsSubscriber.SendFrame), so a slow client just sees skipped
	// frames, not a torn-down connection.
	const writeTimeout = 30 * time.Second
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case f, ok := <-sub.frames:
				if !ok {
					return
				}
				wctx, wcancel := context.WithTimeout(ctx, writeTimeout)
				err := conn.Write(wctx, websocket.MessageBinary, f)
				wcancel()
				if err != nil {
					return
				}
			case n, ok := <-sub.nav:
				if !ok {
					return
				}
				payload, _ := json.Marshal(urlStreamServerMessage{Kind: "nav", URL: n})
				wctx, wcancel := context.WithTimeout(ctx, writeTimeout)
				err := conn.Write(wctx, websocket.MessageText, payload)
				wcancel()
				if err != nil {
					return
				}
			}
		}
	}()

	// Reader loop: parse JSON input events from the client.
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		if mt != websocket.MessageText {
			continue
		}
		var msg urlStreamMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		ev, ok := messageToInputEvent(msg)
		if !ok {
			continue
		}
		// Best-effort: errors are not surfaced to the client (the next
		// frame will reflect whatever the page did with the input).
		_ = s.urlStreamer.ForwardInput(uid, tileID, ev)
	}
	cancel()
	<-writerDone
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// messageToInputEvent maps the wire-shape JSON to the driver's
// InputEvent. Returns false for unknown kinds (which the reader loop
// silently drops).
func messageToInputEvent(m urlStreamMessage) (urldriver.InputEvent, bool) {
	kind := urldriver.InputEventKind(m.Kind)
	switch kind {
	case urldriver.InputMouseMove,
		urldriver.InputMouseDown,
		urldriver.InputMouseUp,
		urldriver.InputMouseWheel,
		urldriver.InputKeyDown,
		urldriver.InputKeyUp,
		urldriver.InputResize:
		// fall through
	default:
		return urldriver.InputEvent{}, false
	}
	return urldriver.InputEvent{
		Kind:      kind,
		X:         m.X,
		Y:         m.Y,
		Button:    m.Button,
		DeltaY:    m.DeltaY,
		Key:       m.Key,
		Code:      m.Code,
		Modifiers: m.Modifiers,
		Width:     m.Width,
		Height:    m.Height,
	}, true
}

// keep package compile happy if errors becomes unreferenced after edits.
var _ = errors.New
var _ = store.ErrNotFound
