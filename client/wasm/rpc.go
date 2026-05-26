//go:build js && wasm

package main

import (
	"encoding/json"
	"errors"
	"syscall/js"
)

// postJSON marshals req as JSON, POSTs it to /rpc/<method>, and decodes the
// response into resp (if non-nil). Returns (status, err).
func postJSON(path string, req any, resp any) (int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	opts := js.Global().Get("Object").New()
	opts.Set("method", "POST")
	hdr := js.Global().Get("Object").New()
	hdr.Set("Content-Type", "application/json")
	opts.Set("headers", hdr)
	opts.Set("body", string(body))

	respValue, err := await(js.Global().Call("fetch", path, opts))
	if err != nil {
		return 0, err
	}
	status := respValue.Get("status").Int()
	textPromise := respValue.Call("text")
	textVal, err := await(textPromise)
	if err != nil {
		return status, err
	}
	bodyText := textVal.String()
	if status == 200 && resp != nil {
		if err := json.Unmarshal([]byte(bodyText), resp); err != nil {
			return status, err
		}
	}
	if status != 200 {
		return status, errors.New(bodyText)
	}
	return status, nil
}

// await blocks until the JS Promise resolves and returns the value, or
// returns an error if the Promise rejects.
func await(p js.Value) (js.Value, error) {
	type result struct {
		val js.Value
		err error
	}
	ch := make(chan result, 1)
	then := js.FuncOf(func(this js.Value, args []js.Value) any {
		var v js.Value
		if len(args) > 0 {
			v = args[0]
		}
		ch <- result{val: v}
		return nil
	})
	defer then.Release()
	catch := js.FuncOf(func(this js.Value, args []js.Value) any {
		msg := "fetch failed"
		if len(args) > 0 {
			msg = args[0].Get("message").String()
		}
		ch <- result{err: errors.New(msg)}
		return nil
	})
	defer catch.Release()
	p.Call("then", then).Call("catch", catch)
	r := <-ch
	return r.val, r.err
}
