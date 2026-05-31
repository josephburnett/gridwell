// Package urldriver defines wire-format input events shared between the
// wasm client and the URL-stream driver. This file has no non-stdlib imports:
// the wasm build (GOOS=js GOARCH=wasm) compiles it, while the rod-backed
// Driver and Session live in files guarded by //go:build !js.
package urldriver

// InputEventKind discriminates between mouse, key, and resize events
// flowing from a URLStream WebSocket client into the driver.
type InputEventKind string

const (
	InputMouseMove   InputEventKind = "mouse_move"
	InputMouseDown   InputEventKind = "mouse_down"
	InputMouseUp     InputEventKind = "mouse_up"
	InputMouseWheel  InputEventKind = "mouse_wheel"
	InputKeyDown     InputEventKind = "key_down"
	InputKeyUp       InputEventKind = "key_up"
	InputResize      InputEventKind = "resize"
	InputHistoryBack InputEventKind = "history_back"
)

// MouseButton names. Empty string means no button (e.g. on move).
const (
	MouseButtonLeft   = "left"
	MouseButtonMiddle = "middle"
	MouseButtonRight  = "right"
)

// InputEvent is one input message routed into Session.Input.
type InputEvent struct {
	Kind      InputEventKind
	X, Y      float64
	Button    string
	DeltaY    float64
	Key       string
	Code      string
	Modifiers int64
	Width     int64
	Height    int64
}
