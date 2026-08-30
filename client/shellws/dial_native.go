//go:build !(js && wasm)

package shellws

import (
	"context"

	"github.com/coder/websocket"
)

// dialConn opens the socket off-browser (the server's seam test, a CLI),
// where the caller must supply the credentials a browser would supply
// itself.
func dialConn(ctx context.Context, addr string, o Options) (*websocket.Conn, error) {
	ws, _, err := websocket.Dial(ctx, addr, &websocket.DialOptions{
		HTTPClient: o.HTTPClient,
		HTTPHeader: o.Header,
	})
	return ws, err
}
