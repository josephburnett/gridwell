package nav

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
)

// fr is rpc.NewFraming for a view a test knows is one.
func fr(cx, cy, zoom float64) rpc.Framing {
	f, err := rpc.NewFraming(cx, cy, zoom)
	if err != nil {
		panic(err)
	}
	return f
}

func viewOf(cx, cy, zoom float64) rpc.View { return rpc.Saved(fr(cx, cy, zoom)) }

func ptr(f rpc.Framing) *rpc.Framing { return &f }

// framed is f left at the view (cx, cy, zoom).
func framed(f pane.Frame, cx, cy, zoom float64) pane.Frame {
	f.View = viewOf(cx, cy, zoom)
	return f
}

// centredOn holds when v is a view centred exactly on (cx, cy).
func centredOn(v rpc.View, cx, cy float64) bool {
	f, ok := v.Framing()
	return ok && f.Cx() == cx && f.Cy() == cy
}
