// Package tracewire is the trace door: where an emitter posts its records,
// the header that ties one gesture to the server work it caused, and the
// record both sides read. It is in the api module so the node and the client
// agree on the shape without importing each other.
package tracewire

import "runtime/debug"

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

	// ClockHeader is the sender's clock when it posted a batch to Path, in
	// unix milliseconds. The node reads the skew off it once per batch and
	// places each record at its CT plus that skew.
	ClockHeader = "Gridwell-Trace-Clock"
)

// The origins. OriginClient and OriginElectron are the only two an emitter may
// claim: the node forces anything else to OriginClient, because a record that
// came through the door did not come from the node. OriginNode and
// OriginPlugin are the node's own, written on records it makes itself.
const (
	OriginClient   = "client"
	OriginElectron = "electron"
	OriginNode     = "node"
	OriginPlugin   = "plugin"
)

// DumpResponse is what DumpPath answers: where the node wrote its ring and
// how many records it held.
type DumpResponse struct {
	Path    string `json:"path"`
	Records int    `json:"records"`
}

// MaxMsg caps Msg. Past it a message is truncated, never dropped: that
// something happened is the part worth keeping.
const MaxMsg = 1024

// Record is one line of the trace. The node stamps Seq and T on receipt, so
// an emitter leaves them zero and they are absent from the line it sends.
type Record struct {
	// Seq is receipt order.
	Seq uint64 `json:"seq,omitempty"`
	// T is when the record happened on the node's clock, unix milliseconds
	// UTC: a node record's emit time, and a sent record's CT moved by its
	// batch's skew (see ClockHeader), so node and client records interleave
	// by T where Seq would order a batch after the node work it preceded.
	T int64 `json:"t,omitempty"`

	Origin string            `json:"origin"`
	Src    string            `json:"src"`
	Kind   string            `json:"kind"`
	Msg    string            `json:"msg"`
	KV     map[string]string `json:"kv,omitempty"`
	CID    string            `json:"cid,omitempty"`
	CT     int64             `json:"ct,omitempty"`
}

// KindBoot is the one record each origin writes when it starts, naming the
// build that wrote everything after it.
const KindBoot = "boot"

// BuildCommit is the vcs revision the go toolchain stamped into this binary,
// "+dirty" when the tree had edits, and "" for a build with no stamp (go test,
// go run).
func BuildCommit() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return commitOf(bi.Settings)
}

func commitOf(settings []debug.BuildSetting) string {
	var rev string
	var dirty bool
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev != "" && dirty {
		rev += "+dirty"
	}
	return rev
}
