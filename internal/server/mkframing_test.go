package server

import "github.com/josephburnett/gridwell/api/rpc"

// mkFraming is rpc.NewFraming for a literal a test knows is a view.
func mkFraming(cx, cy, zoom float64) rpc.Framing {
	f, err := rpc.NewFraming(cx, cy, zoom)
	if err != nil {
		panic(err)
	}
	return f
}
