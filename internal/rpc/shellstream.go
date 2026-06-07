package rpc

// ShellStreamMessage is the client→server text-frame control payload
// for the /rpc/ShellStream WebSocket. Stdin bytes flow as raw binary
// frames (passthrough to bash); this message type only carries control
// signals that need named fields.
//
// Kinds:
//   - "resize":   the pane size changed; cols/rows are character cells.
type ShellStreamMessage struct {
	Kind string `json:"kind"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}
