package trace

import "github.com/josephburnett/gridwell/api/idshape"

// NewRequestID mints the id the shim sends in tracewire.RequestHeader, so the
// client's records of a gesture and the server's records of the work it
// caused name one request. It is the node's own id shape rather than a second
// mint.
func NewRequestID() string { return idshape.NewShortID() }
