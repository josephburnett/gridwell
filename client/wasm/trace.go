//go:build js && wasm

package main

// The client's half of the trace. Everything the shim records goes through
// emit, and every batch reaches the node through one post at a time, on the
// same settle cadence as the other write-outs. A post that fails surfaces
// nothing: a notice is itself a record, so the two would feed each other.

import (
	"bytes"
	"net/http"
	"time"

	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/client/cadence"
	"github.com/josephburnett/gridwell/client/traceevent"
)

// traceContentType is the batch's wire form: one JSON record per line.
const traceContentType = "application/x-ndjson"

// emit is the one entry into the ring. It tries the post the batch rule may
// have earned and arms the clock for the rest.
func (a *App) emit(e traceevent.Event) {
	a.tr.Emit(e.Src, e.Kind, e.Msg, e.KV, time.Now())
	a.flushTrace()
	a.armTraceFlush()
}

// armTraceFlush defers a flush by the trace's own window, coalescing with one
// already waiting. Nothing owed arms nothing, so an idle client holds no timer.
func (a *App) armTraceFlush() {
	if a.tr.PendingCount() > 0 {
		a.persist.sched.traceFlush.Arm(cadence.TraceFlushMs)
	}
}

// flushTrace posts one batch if the pump says it is due. The reply arrives
// long after the gesture, so the acknowledgement is the goroutine's.
func (a *App) flushTrace() {
	batch, done := a.pump.Start(a.tr, time.Now())
	if batch == nil {
		return
	}
	go a.postTraceBatch(batch, done)
}

func (a *App) postTraceBatch(batch []byte, done func(kept bool)) {
	err := a.postTrace(tracewire.Path, batch)
	// The completion first: it is what tells the pump the next post waits for
	// the clock, so the record below cannot start one.
	done(err == nil)
	if err != nil {
		a.emit(traceevent.FlushFailed(err.Error()))
	}
	a.armTraceFlush()
}

// postTrace is the door hop. http.DefaultClient is fetch under wasm, on this
// page's own origin and cookie, the same way every other call reaches the
// node.
func (a *App) postTrace(path string, body []byte) error {
	resp, err := http.Post(a.origin+path, traceContentType, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return httpStatusError(resp.Status)
	}
	return nil
}

// httpStatusError names a door's refusal, which is not a transport error and
// so arrives with no error of its own.
type httpStatusError string

func (e httpStatusError) Error() string { return string(e) }
