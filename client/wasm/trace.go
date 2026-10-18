//go:build js && wasm

package main

// The client's half of the trace. Everything the shim records goes through
// emit, and every batch reaches the node through one post at a time, on the
// same settle cadence as the other write-outs. A routine post that fails
// surfaces nothing: a notice is itself a record, so the two would feed each
// other. Only a dump, which the user asked for, says what it is missing.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"syscall/js"
	"time"

	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/client/trace"
	"github.com/josephburnett/gridwell/client/traceevent"
)

// traceContentType is the batch's wire form: one JSON record per line.
const traceContentType = "application/x-ndjson"

// emit is the shim's entry into the ring; the ring's OnEmit is the flush
// loop (trace.Flusher), so a record made anywhere else posts the same way.
func (a *App) emit(e traceevent.Event) {
	a.tr.Emit(e.Src, e.Kind, e.Msg, e.KV, time.Now())
}

// postTraceBatch is the Flusher's post. The reply arrives long after the
// gesture, so the acknowledgement is the goroutine's.
func (a *App) postTraceBatch(batch []byte, done func(kept bool)) {
	go func() {
		_, err := a.postTrace(tracewire.Path, batch)
		// The answer first: it is what tells the pump the next post waits for
		// the clock, so the record below cannot start one.
		done(err == nil)
		if err != nil {
			a.emit(traceevent.FlushFailed(err.Error()))
		}
	}()
}

// dumpTrace has every half hand the node what it is still holding, then asks
// for the ring on disk. Where it landed, and any half that could not hand
// over, is a notice (trace.DumpNotice): a diagnostic the user cannot find is no
// diagnostic.
func (a *App) dumpTrace() {
	go func() {
		halves := []trace.HandOver{{Origin: tracewire.OriginClient, Lost: a.handOverTrace()}}
		if a.caps.HostTrace {
			halves = append(halves, trace.HandOver{Origin: tracewire.OriginElectron, Lost: a.bridgeHandOverTrace()})
		}
		var dump tracewire.DumpResponse
		body, err := a.postTrace(tracewire.DumpPath, nil)
		if err == nil {
			err = json.Unmarshal(body, &dump)
		}
		sev, msg := trace.DumpNotice(dump.Path, err, halves)
		a.reportErr(sev, traceSource, msg)
	}()
}

// handOverTrace posts every record this client still holds, answering why
// they did not reach the node, empty when they did. It blocks, so it runs only
// off the event loop.
func (a *App) handOverTrace() string {
	batch, done := a.flush.Force()
	if batch == nil {
		return ""
	}
	_, err := a.postTrace(tracewire.Path, batch)
	done(err == nil)
	if err != nil {
		return err.Error()
	}
	return ""
}

// bridgeHandOverTrace waits for the host's TraceClient.handOver, answering why
// its records did not reach the node, empty when they did. It blocks, so it
// runs only off the event loop.
func (a *App) bridgeHandOverTrace() string {
	lost := make(chan string, 1)
	called := a.bridgeVerb("handOverTrace", nil, func(res js.Value) {
		if res.Type() != js.TypeString {
			lost <- "the host answered no verdict"
			return
		}
		lost <- res.String()
	}, func() { lost <- "the host refused the hand-over" })
	if !called {
		return "the host bridge is gone"
	}
	return <-lost
}

// traceSource is the notice row's name for the trace itself, so a second
// dump replaces the first notice instead of stacking one.
const traceSource = "trace"

// postTrace is the API hop, answering the reply body. http.DefaultClient is
// fetch under wasm, on this page's own origin and cookie, the same way every
// other call reaches the node.
func (a *App) postTrace(path string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(http.MethodPost, a.origin+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", traceContentType)
	req.Header.Set(tracewire.ClockHeader, trace.SendClock(time.Now()))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, httpStatusError(resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// httpStatusError names an API's refusal, which is not a transport error and
// so arrives with no error of its own.
type httpStatusError string

func (e httpStatusError) Error() string { return string(e) }
