package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/urldriver"
)

// urlStreamer is the subset of the URL driver this handler requires.
type urlStreamer interface {
	Available() bool
	OpenSession(tileID int64, initialURL string, w, h int64) (urlSession, error)
}

// urlSession is the per-tab handle the URLStream handler holds.
type urlSession interface {
	Input(ev urldriver.InputEvent) error
	Resize(w, h int64) error
	Frames() <-chan []byte
	Navs() <-chan string
	Done() <-chan struct{}
	LastURL() string
	LastTitle() string
	CaptureFinal(ctx context.Context) ([]byte, error)
	Close()
}

// StreamerFromDriver adapts a *urldriver.Driver to the urlStreamer interface.
func StreamerFromDriver(d *urldriver.Driver) urlStreamer {
	return &driverStreamer{d: d}
}

type driverStreamer struct{ d *urldriver.Driver }

func (s *driverStreamer) Available() bool { return s.d.Available() }
func (s *driverStreamer) OpenSession(t int64, url string, w, h int64) (urlSession, error) {
	return s.d.OpenSession(t, url, w, h)
}

func (s *Server) SetURLStreamer(d urlStreamer) {
	s.urlStreamer = d
}

// urlStreamMessage / urlStreamServerMessage alias the shared wire
// types in internal/rpc so the client and server marshal/unmarshal
// identical shapes. The aliases keep call sites in this file terse
// and let `rpc.URLStreamMessage` remain the source of truth.
type (
	urlStreamMessage       = rpc.URLStreamMessage
	urlStreamServerMessage = rpc.URLStreamServerMessage
)

const (
	defaultViewportW = 1280
	defaultViewportH = 800

	minViewportW = 400
	minViewportH = 300

	writeTimeout = 30 * time.Second
)

// urlSessionEntry is the value type for activeURLSessions. It holds the live
// session and a stopOld channel that is closed to signal the currently-attached
// WS handler to exit when a new WS takes over.
type urlSessionEntry struct {
	session urlSession
	stopOld chan struct{} // closed when a new WS replaces the previous one
}

// acquireSession returns the live session for tileID. If an entry already
// exists the previous WS handler is signalled to exit (via stopOld) and the
// same Session is reused — no new tab is opened. If no entry exists a fresh
// tab is opened via urlStreamer.OpenSession.
//
// Returns (session, myStopOld, error). The caller must pass myStopOld to
// releaseSession when the WS handler exits.
func (s *Server) acquireSession(tileID int64, url string) (urlSession, chan struct{}, error) {
	s.activeURLMu.Lock()
	defer s.activeURLMu.Unlock()

	if entry, ok := s.activeURLSessions[tileID]; ok {
		// Takeover: signal the previous WS handler to exit, reuse the session.
		close(entry.stopOld)
		entry.stopOld = make(chan struct{})
		return entry.session, entry.stopOld, nil
	}

	// No existing session — open a new tab.
	sess, err := s.urlStreamer.OpenSession(tileID, url, defaultViewportW, defaultViewportH)
	if err != nil {
		return nil, nil, err
	}
	if s.activeURLSessions == nil {
		s.activeURLSessions = make(map[int64]*urlSessionEntry)
	}
	s.activeURLSessions[tileID] = &urlSessionEntry{
		session: sess,
		stopOld: make(chan struct{}),
	}
	return sess, s.activeURLSessions[tileID].stopOld, nil
}

// releaseSession is called when a WS handler exits. If this handler was still
// the active one (its mySession and myStopOld match the entry), the session is
// closed and the tab terminated. If a takeover already replaced the entry this
// is a no-op — the tab stays alive.
func (s *Server) releaseSession(tileID int64, mySession urlSession, myStopOld chan struct{}) {
	s.activeURLMu.Lock()
	entry, ok := s.activeURLSessions[tileID]
	matches := ok && entry.session == mySession && entry.stopOld == myStopOld
	if matches {
		delete(s.activeURLSessions, tileID)
	}
	s.activeURLMu.Unlock()
	if matches {
		s.closeSession(tileID, mySession)
	}
}

// urlStream is the /rpc/URLStream WebSocket handler.
func (s *Server) urlStream(w http.ResponseWriter, r *http.Request) {
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
	tile, err := s.store.GetTile(r.Context(), tileID)
	if err != nil {
		writeError(w, err)
		return
	}
	if tile.Kind != rpc.KindURL {
		http.Error(w, "not a URL tile", http.StatusBadRequest)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session, myStopOld, err := s.acquireSession(tileID, tile.URLString)
	if err != nil {
		log.Printf("[urlstream] open-err tile=%d err=%v", tileID, err)
		_ = conn.Close(websocket.StatusInternalError, "open session failed")
		return
	}
	log.Printf("[urlstream] open tile=%d", tileID)

	// Cancel the handler context when the session dies, when a takeover
	// evicts this handler, or when the client closes the connection.
	go func() {
		select {
		case <-session.Done():
			cancel()
		case <-myStopOld:
			cancel()
		case <-ctx.Done():
		}
	}()

	writerDone := make(chan struct{})
	go writeLoop(ctx, conn, session, tileID, writerDone)

	readLoop(ctx, conn, session, tileID)

	cancel()
	<-writerDone
	_ = conn.Close(websocket.StatusNormalClosure, "")

	// Close the tab only if this handler is still the active owner.
	// On takeover, releaseSession is a no-op and the tab stays alive.
	s.releaseSession(tileID, session, myStopOld)
}

func writeLoop(ctx context.Context, conn *websocket.Conn, session urlSession, tileID int64, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-session.Done():
			return
		case f := <-session.Frames():
			wctx, wcancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Write(wctx, websocket.MessageBinary, f)
			wcancel()
			if err != nil {
				log.Printf("[urlstream] writer-exit tile=%d kind=binary err=%v", tileID, err)
				return
			}
		case n := <-session.Navs():
			payload, _ := json.Marshal(urlStreamServerMessage{Kind: "nav", URL: n})
			wctx, wcancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Write(wctx, websocket.MessageText, payload)
			wcancel()
			if err != nil {
				log.Printf("[urlstream] writer-exit tile=%d kind=nav err=%v", tileID, err)
				return
			}
		}
	}
}

func readLoop(ctx context.Context, conn *websocket.Conn, session urlSession, tileID int64) {
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			log.Printf("[urlstream] reader-exit tile=%d err=%v", tileID, err)
			return
		}
		if mt != websocket.MessageText {
			continue
		}
		var msg urlStreamMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("[urlstream] bad-json tile=%d err=%v body=%q", tileID, err, string(data))
			continue
		}
		if msg.Kind == "viewport" {
			w, h := msg.Width, msg.Height
			if w < minViewportW {
				w = minViewportW
			}
			if h < minViewportH {
				h = minViewportH
			}
			if err := session.Resize(w, h); err != nil {
				log.Printf("[urlstream] resize-err tile=%d w=%d h=%d err=%v", tileID, w, h, err)
			}
			continue
		}
		ev, ok := messageToInputEvent(msg)
		if !ok {
			log.Printf("[urlstream] unknown-kind tile=%d kind=%q", tileID, msg.Kind)
			continue
		}
		if err := session.Input(ev); err != nil {
			if errors.Is(err, context.Canceled) {
				// A canceled context means EITHER the session is truly
				// dead OR we dispatched to a tab mid-swap: a
				// target=_blank follow cancels the old tab's context,
				// and any input that races the swap fails against it.
				// Only tear down the stream when the session is
				// actually done; otherwise drop this one input and
				// keep reading. Treating every cancel as fatal closed
				// the WS on the first _blank click ("page no longer
				// active").
				select {
				case <-session.Done():
					log.Printf("[urlstream] session-dead tile=%d", tileID)
					return
				default:
					log.Printf("[urlstream] input dropped mid-swap tile=%d kind=%s", tileID, ev.Kind)
					continue
				}
			}
			log.Printf("[urlstream] forward-err tile=%d kind=%s err=%v", tileID, ev.Kind, err)
		}
	}
}

func (s *Server) closeSession(tileID int64, session urlSession) {
	log.Printf("[urlstream] save-and-close tile=%d", tileID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if buf, err := session.CaptureFinal(ctx); err == nil && len(buf) > 0 {
		if err := s.store.SetURLPreview(ctx, tileID, buf); err != nil {
			log.Printf("[urlstream] save-preview-err tile=%d err=%v", tileID, err)
		}
	}
	if u := session.LastURL(); u != "" {
		if err := s.store.SetURLString(ctx, tileID, u); err != nil {
			log.Printf("[urlstream] save-url-err tile=%d err=%v", tileID, err)
		}
	}
	if title := session.LastTitle(); title != "" {
		if err := s.store.SetTileAlt(ctx, tileID, title); err != nil {
			log.Printf("[urlstream] save-alt-err tile=%d err=%v", tileID, err)
		}
	}
	session.Close()
}

func messageToInputEvent(m urlStreamMessage) (urldriver.InputEvent, bool) {
	kind := urldriver.InputEventKind(m.Kind)
	switch kind {
	case urldriver.InputMouseMove,
		urldriver.InputMouseDown,
		urldriver.InputMouseUp,
		urldriver.InputMouseWheel,
		urldriver.InputKeyDown,
		urldriver.InputKeyUp,
		urldriver.InputResize,
		urldriver.InputHistoryBack:
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

