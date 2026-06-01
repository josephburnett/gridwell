// Package urlstream contains pure-Go marshal / parse helpers for the
// WebSocket URL-stream wire protocol. The wasm canvas calls these to
// build outbound messages and parse navigation events; pulling them
// out of the wasm-only file makes them testable under `go test`.
package urlstream

import (
	"encoding/json"

	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/urldriver"
)

// ViewportPayload returns a JSON {"kind":"viewport","width":w,"height":h}
// message ready to send on the WS.
func ViewportPayload(w, h int64) string {
	b, _ := json.Marshal(rpc.URLStreamMessage{
		Kind:   "viewport",
		Width:  w,
		Height: h,
	})
	return string(b)
}

// InputPayload returns the JSON form of an InputEvent — every kind
// (mouse, key, wheel) marshals to one envelope, with omitempty fields
// keeping the wire small.
func InputPayload(ev urldriver.InputEvent) (string, error) {
	b, err := json.Marshal(rpc.URLStreamMessage{
		Kind:      string(ev.Kind),
		X:         ev.X,
		Y:         ev.Y,
		Button:    ev.Button,
		DeltaY:    ev.DeltaY,
		Key:       ev.Key,
		Code:      ev.Code,
		Modifiers: ev.Modifiers,
	})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ParseNavMessage decodes a server→client text frame and, if it's a
// `nav` event, returns the navigated-to URL with ok=true. Any other
// kind (or unparseable payload) returns ok=false.
func ParseNavMessage(payload string) (url string, ok bool) {
	var msg rpc.URLStreamServerMessage
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		return "", false
	}
	if msg.Kind != "nav" {
		return "", false
	}
	return msg.URL, true
}
