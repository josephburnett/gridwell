// Package trace is the node's always-on diagnostic sink: a fixed ring of
// records in memory, one total order, written out on request. It holds no
// node fact — nothing reads it back, deleting it loses nothing the user owns
// — which is why Default is a package-level ring the way log's output is:
// every layer of the node emits, and plumbing a handle through them all would
// buy nothing but the plumbing. A test that needs its own order calls New.
package trace

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Origins. A record that came through the door is the sender's word for which
// of the two it is; anything the node emits itself is OriginNode, and the door
// never lets a sender claim it.
const (
	OriginNode     = "node"
	OriginClient   = "client"
	OriginElectron = "electron"
	OriginPlugin   = "plugin"
)

// MaxMsgBytes caps Msg. Over it the message is truncated, never dropped: a
// long line is still the record of something that happened.
const MaxMsgBytes = 1024

// NodeCapacity is the node ring's size.
const NodeCapacity = 20000

// RequestHeader carries the client's request id across the door, so a gesture
// and the rpcs it caused join on kv["req"].
const RequestHeader = "Gridwell-Request"

// Record is one line of the trace. Seq and T are the ring's, stamped on
// arrival; Cid and Ct are the sender's and stay zero on a node record.
type Record struct {
	Seq    int64             `json:"seq"`
	T      string            `json:"t"`
	Origin string            `json:"origin"`
	Src    string            `json:"src"`
	Kind   string            `json:"kind"`
	Msg    string            `json:"msg"`
	KV     map[string]string `json:"kv,omitempty"`
	Cid    string            `json:"cid"`
	Ct     int64             `json:"ct"`
}

// Ring is a fixed-size circular buffer of records. Emit overwrites the oldest.
type Ring struct {
	mu   sync.Mutex
	buf  []Record
	next int   // where the next record lands
	n    int   // records held, up to len(buf)
	seq  int64 // the one total order
	now  func() time.Time
}

// New returns a ring of capacity records. A capacity below one holds one.
func New(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{buf: make([]Record, capacity), now: time.Now}
}

// def is the node's ring; see the package comment.
var def = New(NodeCapacity)

// Emit stamps a node record onto the default ring.
func Emit(src, kind, msg string, kv map[string]string) {
	def.Emit(Record{Origin: OriginNode, Src: src, Kind: kind, Msg: msg, KV: kv})
}

// Default is the node's one ring: what the door dumps and every Emit below
// lands in.
func Default() *Ring { return def }

// Emit stamps seq and t onto rec and appends it. It takes a mutex and writes a
// slot: nothing here may block a caller on the node's write path.
func (r *Ring) Emit(rec Record) {
	if rec.Origin == "" {
		rec.Origin = OriginNode
	}
	rec.Msg = capMsg(rec.Msg)
	r.mu.Lock()
	r.seq++
	rec.Seq = r.seq
	rec.T = r.now().UTC().Format(time.RFC3339Nano)
	r.buf[r.next] = rec
	r.next = (r.next + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
	r.mu.Unlock()
}

// Snapshot is every record the ring still holds, oldest seq first.
func (r *Ring) Snapshot() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, 0, r.n)
	start := (r.next - r.n + len(r.buf)) % len(r.buf)
	for i := 0; i < r.n; i++ {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}

// Ingest reads JSON lines — records without seq or t — and emits each one. A
// malformed line stops the read with its line number, and every good line
// before it is already in the ring: a sender that garbles one record still
// gets the rest of its gesture traced.
func (r *Ring) Ingest(in io.Reader) (n int, err error) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(text), &rec); err != nil {
			return n, fmt.Errorf("line %d: %w", line, err)
		}
		rec.Origin = senderOrigin(rec.Origin)
		r.Emit(rec)
		n++
	}
	if err := sc.Err(); err != nil {
		return n, err
	}
	return n, nil
}

// senderOrigin is what a sender may call itself. Anything else, "node"
// included, reads as the client: a record that came through the door did not
// come from the node.
func senderOrigin(origin string) string {
	if origin == OriginElectron {
		return OriginElectron
	}
	return OriginClient
}

// Dump writes the ring to <dir>/trace-<yyyymmdd-hhmmss>.jsonl in seq order and
// returns the absolute path. The ring is not cleared: a dump is a reading, not
// a handover.
func (r *Ring) Dump(dir string, now time.Time) (path string, n int, err error) {
	if dir == "" {
		return "", 0, fmt.Errorf("trace: no dump directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	path, err = filepath.Abs(filepath.Join(dir, "trace-"+now.UTC().Format("20060102-150405")+".jsonl"))
	if err != nil {
		return "", 0, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", 0, err
	}
	w := bufio.NewWriter(f)
	records := r.Snapshot()
	for _, rec := range records {
		blob, err := json.Marshal(rec)
		if err != nil {
			f.Close()
			return "", 0, err
		}
		w.Write(blob)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return "", 0, err
	}
	if err := f.Close(); err != nil {
		return "", 0, err
	}
	return path, len(records), nil
}

// capMsg truncates to MaxMsgBytes, dropping a rune the cut split so the line
// stays valid UTF-8.
func capMsg(msg string) string {
	if len(msg) <= MaxMsgBytes {
		return msg
	}
	msg = msg[:MaxMsgBytes]
	for len(msg) > 0 && !utf8.ValidString(msg) {
		msg = msg[:len(msg)-1]
	}
	return msg
}

// LogWriter turns each line written to it into a node record, so every
// log.Printf on the node lands in the ring without a second spelling at the
// call site.
func LogWriter(r *Ring) io.Writer { return lineWriter{r, OriginNode, "log"} }

// PluginWriter is one plugin subprocess's stderr, under its own id: the
// subprocess does not reach the node's log, so its lines arrive here instead.
func PluginWriter(r *Ring, pluginID string) io.Writer {
	return lineWriter{r, OriginPlugin, pluginID}
}

type lineWriter struct {
	r      *Ring
	origin string
	src    string
}

func (w lineWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			w.r.Emit(Record{Origin: w.origin, Src: w.src, Kind: "log", Msg: line})
		}
	}
	return len(p), nil
}
