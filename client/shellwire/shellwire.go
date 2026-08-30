// Package shellwire is the shell transport's wire grammar — the ONE place
// the client and the server agree on how PTY bytes cross the web door.
//
// Owner decision (docs/simplify-plan.md, 2026-08-29): "Shells are a
// WebSocket on the web door", so the primitive set is identical on every
// host. Before that, PTY bytes rode a second client stack (Electron main →
// gRPC → the federation socket) and only the desktop had shells; every
// other primitive already rode the page's own origin.
//
// The grammar, in full:
//
//	GET <origin>/shell?tile_id=<qualified>&cols=N&rows=N   (Upgrade: websocket)
//	  · gated by the SAME auth cookie as every other page request
//	    (internal/server/auth.go) and strict same-origin;
//	  · BINARY frames, both directions, are raw PTY bytes — keystrokes up,
//	    terminal output down. Nothing wraps them: a shell is a byte pipe.
//	  · TEXT frames are JSON Control messages. Up: "resize". Down: "exit",
//	    sent once, immediately before the close, carrying the verdict the
//	    gRPC status used to carry (why it ended, and whether the session
//	    itself is GONE — the fact the refresh affordance reads).
//
// Both ends read the codec here rather than spelling the JSON twice, and a
// seam test dials the real handler with these very functions
// (internal/server/shell_door_seam_test.go).
package shellwire

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
)

// Path is the door's address on the web mux.
const Path = "/shell"

// ReadLimit bounds ONE frame, at both ends. PTY output arrives in kilobyte
// chunks and a keystroke frame is a handful of bytes — but a PASTE is one
// frame of whatever the user pasted, and the library default (32 KiB)
// would tear the socket down on a large one.
const ReadLimit = 8 << 20

// Query keys of the bind. The bind rides the handshake rather than a first
// frame so the attach is atomic with the upgrade: there is no window in
// which a socket is open but bound to nothing.
const (
	QueryTileID = "tile_id"
	QueryCols   = "cols"
	QueryRows   = "rows"
)

// Control kinds. The set is closed: anything else is a protocol error.
const (
	// KindResize (client → server) carries a new PTY winsize.
	KindResize = "resize"
	// KindExit (server → client) ends the stream: why, and whether the
	// session is gone for good.
	KindExit = "exit"
)

// Control is a text frame. One struct both directions — the fields a kind
// does not use are omitted, so the JSON of each kind is exactly its own
// facts.
type Control struct {
	Kind string `json:"kind"`
	// Cols/Rows: KindResize.
	Cols int `json:"cols,omitempty"`
	Rows int `json:"rows,omitempty"`
	// Message/SessionGone: KindExit. SessionGone is the server's definitive
	// "this PTY session no longer exists" (as opposed to a transport
	// failure) — the client flips the refresh affordance off for it.
	Message     string `json:"message,omitempty"`
	SessionGone bool   `json:"session_gone,omitempty"`
}

// AttachURL is the address a client dials to attach to tileID's PTY.
// origin is the page's own http(s) origin; the scheme is swapped to ws(s)
// because a WebSocket URL may carry no other. cols/rows are the terminal's
// initial size (the serving plugin clamps them — shellsvc.ClampSize is the
// one owner of the bounds, so nothing is re-clamped here).
func AttachURL(origin, tileID string, cols, rows int) (string, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http", "ws":
		u.Scheme = "ws"
	case "https", "wss":
		u.Scheme = "wss"
	default:
		return "", errors.New("shellwire: origin scheme must be http(s): " + origin)
	}
	if u.Host == "" {
		return "", errors.New("shellwire: origin has no host: " + origin)
	}
	u.Path = Path
	q := url.Values{}
	q.Set(QueryTileID, tileID)
	q.Set(QueryCols, strconv.Itoa(cols))
	q.Set(QueryRows, strconv.Itoa(rows))
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String(), nil
}

// Attach is the bind AttachURL encodes, as the door reads it back.
type Attach struct {
	TileID string
	// Cols/Rows are 0 when absent or unparseable: the serving plugin
	// substitutes its defaults, so the door never invents a size.
	Cols int
	Rows int
}

// ParseAttach reads the bind out of a request's query string. A missing
// tile_id is the one hard error — there is nothing to attach to.
func ParseAttach(q url.Values) (Attach, error) {
	a := Attach{TileID: q.Get(QueryTileID)}
	if a.TileID == "" {
		return Attach{}, errors.New("shellwire: missing " + QueryTileID)
	}
	a.Cols = atoiOrZero(q.Get(QueryCols))
	a.Rows = atoiOrZero(q.Get(QueryRows))
	return a, nil
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// EncodeResize builds the client→server winsize frame.
func EncodeResize(cols, rows int) []byte {
	return mustJSON(Control{Kind: KindResize, Cols: cols, Rows: rows})
}

// EncodeExit builds the server→client end-of-stream frame.
func EncodeExit(message string, sessionGone bool) []byte {
	return mustJSON(Control{Kind: KindExit, Message: message, SessionGone: sessionGone})
}

// DecodeControl parses a text frame. Both ends use it, so a frame either
// end cannot read is a failure at the sender, not a silent drop.
func DecodeControl(b []byte) (Control, error) {
	var c Control
	if err := json.Unmarshal(b, &c); err != nil {
		return Control{}, err
	}
	switch c.Kind {
	case KindResize, KindExit:
		return c, nil
	default:
		return Control{}, errors.New("shellwire: unknown control kind " + strconv.Quote(c.Kind))
	}
}

// mustJSON: the Control struct has no field json cannot marshal, so an
// error here would be a compile-time-shaped bug, not a runtime condition.
func mustJSON(c Control) []byte {
	b, err := json.Marshal(c)
	if err != nil {
		panic("shellwire: marshal control: " + err.Error())
	}
	return b
}
