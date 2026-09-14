// Package shellws dials the web door's /shell WebSocket and presents it as a
// shellstream.Dialer. It is the one client implementation of
// client/shellwire's grammar, used by the wasm client and by the server's seam
// tests off-browser. A unit test here would assert against a second spelling
// of the protocol, so the package has none.
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

// DefaultWriteTimeout bounds one frame write, so a wedged socket surfaces as an
// error instead of parking the terminal's keystrokes forever. It is exported
// because the seam test that binds it dials this package from outside.
const DefaultWriteTimeout = 30 * time.Second

type Options struct {
	// Origin is the page's own http(s) origin; the door is same-origin by
	// construction.
	Origin string
	// HTTPClient and Header are honored off-browser only: a browser attaches
	// its own cookies and forbids setting handshake headers.
	HTTPClient *http.Client
	Header     http.Header
	// WriteTimeout overrides DefaultWriteTimeout for one dialer; zero takes
	// the default.
	WriteTimeout time.Duration
}

func Dialer(o Options) shellstream.Dialer {
	return func(tileID string, cols, rows int, onData func([]byte), onEnd func(string, bool)) shellstream.Handle {
		ctx, cancel := context.WithCancel(context.Background())
		bound := o.WriteTimeout
		if bound == 0 {
			bound = DefaultWriteTimeout
		}
		c := &conn{wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel, onEnd: onEnd, writeBound: bound}
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

// conn queues frames rather than writing inline, because a keystroke can
// arrive before the socket has finished opening and a dropped keystroke is a
// lost character. One goroutine owns the write side.
type conn struct {
	mu     sync.Mutex
	ws     *websocket.Conn
	queue  []frame
	closed bool

	wake   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	// writeBound is this conn's frame-write bound; see DefaultWriteTimeout.
	writeBound time.Duration

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
			wctx, wcancel := context.WithTimeout(c.ctx, c.writeBound)
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

// Write copies the slice, because the caller may reuse it.
func (c *conn) Write(data []byte) {
	c.push(websocket.MessageBinary, append([]byte(nil), data...))
}

func (c *conn) Resize(cols, rows int) {
	c.push(websocket.MessageText, shellwire.EncodeResize(cols, rows))
}

// Close detaches from this side. The end is still delivered exactly once.
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
		// browser arrives only through the JS event loop Close is called from.
		// Waiting here would block the loop that must deliver the answer.
		go func() { _ = ws.Close(websocket.StatusNormalClosure, "") }()
	}
	c.end("", false)
}
