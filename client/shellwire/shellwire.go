// Package shellwire is the shell transport's wire grammar, the one place the
// client and the server agree on how PTY bytes cross the web door:
//
//	GET <origin>/shell?tile_id=<qualified>&cols=N&rows=N&gridwell_build=B   (Upgrade: websocket)
//	  · gated by the same auth cookie as every other page request
//	    (internal/server/auth.go) and strict same-origin;
//	  · binary frames both ways are raw PTY bytes, nothing wrapping them;
//	  · text frames are JSON Control messages. Up "resize", down "exit", sent
//	    once immediately before the close.
//
// internal/server/shell_door_seam_test.go dials the real handler with these.
package shellwire

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"

	"github.com/josephburnett/gridwell/api/tracewire"
)

// Path is the door's address on the web mux.
const Path = "/shell"

// ReadLimit bounds one frame at both ends. A paste is one frame, and the
// library default of 32 KiB would tear the socket down on a large one.
const ReadLimit = 8 << 20

// Query keys of the bind. The bind rides the handshake rather than a first
// frame, so no socket is ever open but bound to nothing.
const (
	QueryTileID = "tile_id"
	QueryCols   = "cols"
	QueryRows   = "rows"
)

// Control kinds. The set is closed: anything else is a protocol error.
const (
	// KindResize (client → server) carries a new PTY winsize.
	KindResize = "resize"
	// KindExit (server → client) ends the stream.
	KindExit = "exit"
)

// Control is a text frame, one struct for both directions. Fields a kind does
// not use are omitted, so each kind's JSON is exactly its own facts.
type Control struct {
	Kind string `json:"kind"`
	// KindResize.
	Cols int `json:"cols,omitempty"`
	Rows int `json:"rows,omitempty"`
	// KindExit. SessionGone means the PTY session is gone rather than the
	// transport failing, and the client flips the refresh affordance off.
	Message     string `json:"message,omitempty"`
	SessionGone bool   `json:"session_gone,omitempty"`
	// StaleBuild is the node's build when the door refused a page of another
	// one (gwerr.StaleBuild), the socket's form of that verdict.
	StaleBuild string `json:"stale_build,omitempty"`
}

// AttachURL is the address a client dials to attach to tileID's PTY. origin is
// the page's own http(s) origin, whose scheme is swapped to ws(s); build is
// the page's, which a browser cannot send as a header.
// shellsvc.ClampSize owns the size bounds, so nothing is re-clamped here.
func AttachURL(origin, build, tileID string, cols, rows int) (string, error) {
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
	q.Set(tracewire.BuildQuery, build)
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String(), nil
}

// Attach is the bind AttachURL encodes, as the door reads it back.
type Attach struct {
	TileID string
	// Cols/Rows are 0 when absent or unparseable; the serving plugin
	// substitutes its defaults, so the door never invents a size.
	Cols int
	Rows int
}

// ParseAttach reads the bind out of a request's query string. A missing
// tile_id is the one hard error.
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

func EncodeResize(cols, rows int) []byte {
	return mustJSON(Control{Kind: KindResize, Cols: cols, Rows: rows})
}

func EncodeExit(message string, sessionGone bool) []byte {
	return mustJSON(Control{Kind: KindExit, Message: message, SessionGone: sessionGone})
}

// EncodeStaleBuild is the exit the door sends a page of another build before
// any PTY is touched.
func EncodeStaleBuild(message, node string) []byte {
	return mustJSON(Control{Kind: KindExit, Message: message, StaleBuild: node})
}

// DecodeControl is used by both ends, so an unreadable frame is a failure at
// the sender rather than a silent drop.
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

// mustJSON panics because Control has no field json can fail to marshal.
func mustJSON(c Control) []byte {
	b, err := json.Marshal(c)
	if err != nil {
		panic("shellwire: marshal control: " + err.Error())
	}
	return b
}
