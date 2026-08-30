// Package shellws dials the web door's /shell WebSocket and presents it as a
// shellstream.Dialer. It is the one client implementation of
// client/shellwire's grammar: the wasm client uses it in the browser, and the
// server's seam test uses it off-browser, so the test exercises the code the
// app runs rather than a second spelling of the protocol.
//
// When a stream opens, closes, or reports its end is client/shellstream's
// business; this package only turns those calls into frames.
package shellws

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/josephburnett/gridwell/client/shellstream"
	"github.com/josephburnett/gridwell/client/shellwire"
)

// writeTimeout bounds one frame write, so a wedged socket surfaces as an
// error instead of parking the terminal's keystrokes forever.
const writeTimeout = 30 * time.Second

// Options configure the dialer.
type Options struct {
	// Origin is the page's own http(s) origin. The door is same-origin by
	// construction: it is the page's own server.
	Origin string
	// HTTPClient and Header are honored off-browser only (tests, a CLI): a
	// browser attaches the page's own cookies to a same-origin upgrade and
	// forbids setting handshake headers, so the wasm client leaves both nil
	// and is authenticated by the cookie that served the page.
	HTTPClient *http.Client
	Header     http.Header
}

// Dialer returns the shellstream.Dialer for these options.
func Dialer(o Options) shellstream.Dialer {
	return func(tileID string, cols, rows int, onData func([]byte), onEnd func(string, bool)) shellstream.Handle {
		ctx, cancel := context.WithCancel(context.Background())
		c := &conn{wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel, onEnd: onEnd}
		addr, err := shellwire.AttachURL(o.Origin, tileID, cols, rows)
		if err != nil {
			c.end("shell address: "+err.Error(), false)
			return c
		}
		go c.run(addr, o, onData)
		return c
	}
}

type frame struct {
	typ  websocket.MessageType
	data []byte
}

// conn is one attachment. Frames are queued rather than written inline:
// keystrokes arrive from the terminal's callback the instant the user types,
// which can be before the socket has finished opening, and a dropped
// keystroke is a lost character. One goroutine owns the write side.
type conn struct {
	mu     sync.Mutex
	ws     *websocket.Conn
	queue  []frame
	closed bool

	wake   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc

	once  sync.Once
	onEnd func(string, bool)
}

// end delivers the stream's end exactly once and tears the context down.
func (c *conn) end(message string, sessionGone bool) {
	c.once.Do(func() {
		c.cancel()
		c.onEnd(message, sessionGone)
	})
}

func (c *conn) run(addr string, o Options, onData func([]byte)) {
	ws, err := dialConn(c.ctx, addr, o)
	if err != nil {
		c.end("shell connect: "+err.Error(), false)
		return
	}
	ws.SetReadLimit(shellwire.ReadLimit)
	c.mu.Lock()
	if c.closed { // closed while the handshake was in flight
		c.mu.Unlock()
		_ = ws.CloseNow()
		c.end("", false)
		return
	}
	c.ws = ws
	c.mu.Unlock()
	// Anything queued while connecting goes out now.
	c.signal()
	go c.writeLoop()

	for {
		typ, data, rerr := ws.Read(c.ctx)
		if rerr != nil {
			if c.isClosed() {
				c.end("", false) // this side asked; the caller already knows
				return
			}
			c.end("shell stream: "+rerr.Error(), false)
			return
		}
		switch typ {
		case websocket.MessageBinary:
			onData(data)
		case websocket.MessageText:
			ctl, derr := shellwire.DecodeControl(data)
			if derr != nil {
				c.end("shell protocol: "+derr.Error(), false)
				return
			}
			if ctl.Kind == shellwire.KindExit {
				// The door's last word: why it ended, and whether the
				// session is gone for good.
				c.end(ctl.Message, ctl.SessionGone)
				return
			}
		}
	}
}

func (c *conn) writeLoop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.wake:
		}
		for {
			f, ok := c.pop()
			if !ok {
				break
			}
			wctx, wcancel := context.WithTimeout(c.ctx, writeTimeout)
			err := c.ws.Write(wctx, f.typ, f.data)
			wcancel()
			if err != nil {
				if !c.isClosed() {
					c.end("shell write: "+err.Error(), false)
				}
				return
			}
		}
	}
}

func (c *conn) pop() (frame, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) == 0 {
		return frame{}, false
	}
	f := c.queue[0]
	c.queue = c.queue[1:]
	return f, true
}

func (c *conn) push(typ websocket.MessageType, data []byte) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.queue = append(c.queue, frame{typ: typ, data: data})
	connected := c.ws != nil
	c.mu.Unlock()
	if connected {
		c.signal()
	}
}

func (c *conn) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *conn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Write sends keystroke bytes as one binary frame. The slice is copied: it
// comes from the terminal's callback and the caller may reuse it.
func (c *conn) Write(data []byte) {
	c.push(websocket.MessageBinary, append([]byte(nil), data...))
}

// Resize sends a winsize control frame.
func (c *conn) Resize(cols, rows int) {
	c.push(websocket.MessageText, shellwire.EncodeResize(cols, rows))
}

// Close detaches from this side. The end is still delivered (exactly once);
// the caller suppresses it, because the caller asked.
func (c *conn) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	ws := c.ws
	c.mu.Unlock()
	if ws != nil {
		// The close handshake waits for the peer's close frame, which in a
		// browser can only arrive through the JS event loop — and Close is
		// called from that loop (a pane teardown inside a click handler).
		// Waiting here would block the loop that must deliver the answer and
		// freeze the page permanently. Hand the wait to a goroutine; the
		// caller returns immediately.
		go func() { _ = ws.Close(websocket.StatusNormalClosure, "") }()
	}
	c.end("", false)
}
