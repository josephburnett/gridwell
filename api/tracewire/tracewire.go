// Package tracewire is the trace door: where an emitter posts its records,
// the header that ties one gesture to the server work it caused, and the
// record both sides read. It is in the api module so the node and the client
// agree on the shape without importing each other.
package tracewire

const (
	// Path takes JSON lines of Record, one per line, and answers 204 once the
	// node has kept them. It rides the web mux behind the same cookie as
	// everything else.
	Path = "/trace"

	// DumpPath answers where the node wrote its ring and how many records it
	// held.
	DumpPath = "/trace/dump"

	// RequestHeader carries the emitter's request id on the calls a gesture
	// makes, so the client's records and the server's name the same work.
	RequestHeader = "Gridwell-Request"
)

// Origin is the emitter's claim about which process it is. The node stamps no
// origin: it is the one fact a record cannot be told from outside.
const (
	OriginClient   = "client"
	OriginElectron = "electron"
)

// MaxMsg caps Msg. Past it a message is truncated, never dropped: that
// something happened is the part worth keeping.
const MaxMsg = 1024

// Record is one line of the trace. The node stamps Seq and T on receipt, so
// an emitter leaves them zero and they are absent from the line it sends.
type Record struct {
	Seq uint64 `json:"seq,omitempty"`
	T   int64  `json:"t,omitempty"`

	Origin string            `json:"origin"`
	Src    string            `json:"src"`
	Kind   string            `json:"kind"`
	Msg    string            `json:"msg"`
	KV     map[string]string `json:"kv,omitempty"`
	CID    string            `json:"cid,omitempty"`
	CT     int64             `json:"ct,omitempty"`
}
