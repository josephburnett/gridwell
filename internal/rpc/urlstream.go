package rpc

// URLStreamMessage is the client→server WebSocket message for the URL
// stream. One struct carries every event kind (mouse, key, wheel,
// viewport resize); kind-irrelevant fields use `omitempty`. Defined
// once here so client and server marshal/unmarshal the same shape.
type URLStreamMessage struct {
	Kind      string  `json:"kind"`
	X         float64 `json:"x,omitempty"`
	Y         float64 `json:"y,omitempty"`
	Button    string  `json:"button,omitempty"`
	DeltaY    float64 `json:"delta_y,omitempty"`
	Key       string  `json:"key,omitempty"`
	Code      string  `json:"code,omitempty"`
	Modifiers int64   `json:"modifiers,omitempty"`
	Width     int64   `json:"width,omitempty"`
	Height    int64   `json:"height,omitempty"`
}

// URLStreamServerMessage is the server→client WebSocket message for
// navigation events streamed back to the embedding page.
type URLStreamServerMessage struct {
	Kind string `json:"kind"`
	URL  string `json:"url,omitempty"`
}
