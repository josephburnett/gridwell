package server_test

import "github.com/josephburnett/gridwell/api/rpc"

// mkFramingExt is rpc.NewFraming for a literal a test knows is a view.
func mkFramingExt(cx, cy, zoom float64) rpc.Framing {
	f, err := rpc.NewFraming(cx, cy, zoom)
	if err != nil {
		panic(err)
	}
	return f
}
