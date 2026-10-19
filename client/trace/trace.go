// Package trace is the client's half of the always-on trace: a ring of
// records the shim posts to the node's trace API, so one dump shows a
// gesture from the press through the server's work to the frame.
package trace

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/josephburnett/gridwell/api/tracewire"
)

// DefaultCapacity is many gestures' worth of history.
const DefaultCapacity = 2000

// A loss is itself a record, under the trace's own name.
const (
	dropSrc  = "trace"
	dropKind = "drop"
)

// Client is not safe for concurrent use, the client being single-threaded.
type Client struct {
	// OnEmit says a record is owed, so the owner can arm its flush; the rpc
	// interceptor emits here directly, not through the shim.
	OnEmit func()

	cid  string
	ring []tracewire.Record
	// A serial counts every record ever emitted, so an acknowledgement
	// survives a wrap. base is the serial of the oldest record held, at ring
	// index first; an eviction carries acked along with it, so the range
	// still owed is always [acked, base+count).
	first int
	count int
	base  uint64
	acked uint64
	// out is the end of the last batch handed out, for UnsentBatch.
	out     uint64
	dropped int
	lastCT  int64
}

// New mints a ring of capacity records, each stamped with cid, the id that
// says which client emitted them: the shim passes its session
// (rpc.NewSession), the id the node files its view under.
func New(capacity int, cid string) *Client {
	if capacity < 1 {
		capacity = DefaultCapacity
	}
	return &Client{cid: cid, ring: make([]tracewire.Record, capacity)}
}

// Emit appends one record. now is the client's own clock, which the node
// keeps beside the one it stamps on receipt.
func (c *Client) Emit(src, kind, msg string, kv map[string]string, now time.Time) {
	c.lastCT = now.UnixMilli()
	emit(c, src, kind, msg, kv, c.lastCT)
	if c.OnEmit != nil {
		c.OnEmit()
	}
}

func emit(c *Client, src, kind, msg string, kv map[string]string, ct int64) {
	if c.count == len(c.ring) {
		// An evicted record the node never kept is a hole the next batch tells.
		if c.acked <= c.base {
			c.dropped++
			c.acked = c.base + 1
		}
		c.first = (c.first + 1) % len(c.ring)
		c.base++
		c.count--
	}
	c.ring[(c.first+c.count)%len(c.ring)] = tracewire.Record{
		Origin: tracewire.OriginClient,
		Src:    src,
		Kind:   kind,
		Msg:    truncate(msg),
		KV:     copyKV(kv),
		CID:    c.cid,
		CT:     ct,
	}
	c.count++
}

// CID is the id this client stamps on every record it makes, so a dump says
// which page's story a line belongs to.
func (c *Client) CID() string { return c.cid }

// PendingCount is how many records the node has not acknowledged.
func (c *Client) PendingCount() int { return int(c.base + uint64(c.count) - c.acked) }

// PendingBatch is every unacknowledged record as JSON lines, and the ack to
// call once the API has answered. Unacked records ride the next batch, so a
// failed post loses nothing.
func (c *Client) PendingBatch() ([]byte, func()) {
	if n := c.dropped; n > 0 {
		// The evictions since the last batch are one record, which can itself
		// evict another; that loss rides the next batch.
		c.dropped = 0
		emit(c, dropSrc, dropKind, "records dropped before the node saw them",
			map[string]string{"n": strconv.Itoa(n)}, c.lastCT)
	}
	batch, end := c.batchFrom(c.acked)
	if batch == nil {
		return nil, func() {}
	}
	c.out = end
	// The ack closes over this batch's end, so a record emitted while the
	// post is in flight stays pending.
	return batch, func() {
		if end > c.acked {
			c.acked = end
		}
	}
}

// UnsentBatch is every record emitted since the last batch handed out, for
// the page's last post while that batch is still out: the page dies before
// either is answered, and sending the out batch's records twice would put
// them in the node's ring twice.
func (c *Client) UnsentBatch() []byte {
	b, _ := c.batchFrom(max(c.acked, c.out))
	return b
}

func (c *Client) batchFrom(from uint64) ([]byte, uint64) {
	end := c.base + uint64(c.count)
	from = max(from, c.base)
	if from >= end {
		return nil, end
	}
	recs := make([]tracewire.Record, 0, end-from)
	for s := from; s < end; s++ {
		recs = append(recs, c.ring[(c.first+int(s-c.base))%len(c.ring)])
	}
	return Marshal(recs), end
}

// SendClock is tracewire.ClockHeader's value for a batch posted at now: the
// same clock, in the same unit, as the CT Emit stamps.
func SendClock(now time.Time) string { return strconv.FormatInt(now.UnixMilli(), 10) }

// Marshal is the wire body: one record per line, newline-terminated. HTML
// escaping is off, so an address reads as the emitter wrote it.
func Marshal(recs []tracewire.Record) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, r := range recs {
		if err := enc.Encode(r); err != nil {
			panic("trace: a record of strings and ints failed to encode: " + err.Error())
		}
	}
	return buf.Bytes()
}

func truncate(msg string) string {
	if len(msg) <= tracewire.MaxMsg {
		return msg
	}
	cut := tracewire.MaxMsg
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut]
}

// copyKV keeps the caller's map out of the ring.
func copyKV(kv map[string]string) map[string]string {
	if len(kv) == 0 {
		return nil
	}
	out := make(map[string]string, len(kv))
	for k, v := range kv {
		out[k] = v
	}
	return out
}
