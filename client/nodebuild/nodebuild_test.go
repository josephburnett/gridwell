package nodebuild

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/tracewire"
)

func TestDecide(t *testing.T) {
	for _, tc := range []struct {
		name               string
		reloaded, accepted bool
		want               Verdict
	}{
		{"the node changed under a running page", false, true, Offer},
		{"a tab opened on a node newer than its cached client", false, false, Offer},
		{"a reloaded page the node has answered, refused after a later restart", true, true, Offer},
		{"a reload just served it and the node still refuses", true, false, Stuck},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.reloaded, tc.accepted); got != tc.want {
				t.Errorf("Decide(%v,%v) = %v, want %v", tc.reloaded, tc.accepted, got, tc.want)
			}
		})
	}
}

// The one notice names what reloading costs, by kind and count, never
// dropping it silently.
func TestNotice(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    Verdict
		ops  []string
		text int
		want string
	}{
		{"nothing to lose", Offer, nil, 0, Updated},
		{"one write", Offer, []string{"SetFraming"}, 0, Updated + " 1 view change was not saved."},
		{"kinds that are one kind count together", Offer, []string{"PlaceTile", "PaneLayout"}, 0,
			Updated + " 2 layout changes were not saved."},
		{"kinds in drain order", Offer, []string{"SetFraming", "PlaceTile", "SetTextView", "SetFrozen"}, 0,
			Updated + " 2 view changes, 1 layout change and 1 page capture were not saved."},
		{"an op with no name is named by itself", Offer, []string{"SetFraming", "Mystery"}, 0,
			Updated + " 1 view change and 1 Mystery were not saved."},
		{"text the node never saved", Offer, []string{"Content"}, 1,
			Updated + " 1 text edit was not saved. Copy your text first."},
		{"text not yet parked", Offer, nil, 2, Updated + " Copy your text first."},
		{"a reload cannot fix it", Stuck, []string{"SetFraming"}, 1,
			"the node runs 0123456 but serves a client of another build: rebuild the client"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Notice(tc.v, "0123456789abcdef", tc.ops, tc.text); got != tc.want {
				t.Errorf("Notice =\n %q\nwant\n %q", got, tc.want)
			}
		})
	}
}

// Only the door's verdict is a refusal; an answer is an acceptance; a
// transport failure is neither, because the node said nothing.
func TestGateHears(t *testing.T) {
	var heard []string
	g := New("pagebuild", func(node string) { heard = append(heard, node) })
	g.Hear(errors.New("dial tcp: connection refused"))
	g.Hear(connect.NewError(connect.CodeFailedPrecondition, errors.New("changed on disk since it was read")))
	if g.Accepted() || len(heard) != 0 {
		t.Fatalf("accepted=%v heard=%v after no answer and a plain refusal", g.Accepted(), heard)
	}
	g.Hear(gwerr.StaleBuild("nodebuild", "pagebuild"))
	if len(heard) != 1 || heard[0] != "nodebuild" || g.Accepted() {
		t.Fatalf("heard=%v accepted=%v, want the node's build and no acceptance", heard, g.Accepted())
	}
	g.Hear(nil)
	if !g.Accepted() {
		t.Error("an answered call is not an acceptance")
	}
}

func TestBeaconPathCarriesTheBuild(t *testing.T) {
	g := New("abc+dirty", nil)
	for _, p := range []string{"/gridwell.v1.Gridwell/SetTile", "/x?y=1"} {
		u, err := url.Parse(g.BeaconPath(p))
		if err != nil {
			t.Fatal(err)
		}
		if b := u.Query().Get(tracewire.BuildQuery); b != "abc+dirty" {
			t.Errorf("%s: build %q, want the page's", p, b)
		}
	}
}

// The interceptor stamps the header a unary call carries; the door seam test
// (internal/server/build_gate_test.go) crosses the wire with it.
func TestInterceptorStampsUnary(t *testing.T) {
	g := New("pagebuild", func(string) {})
	var got http.Header
	call := g.Interceptor().WrapUnary(func(_ context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		got = req.Header()
		return nil, nil
	})
	req := connect.NewRequest(&struct{}{})
	if _, err := call(context.Background(), clientSpec{req}); err != nil {
		t.Fatal(err)
	}
	if got.Get(tracewire.BuildHeader) != "pagebuild" || !g.Accepted() {
		t.Errorf("header %q accepted %v", got.Get(tracewire.BuildHeader), g.Accepted())
	}
}

type clientSpec struct{ *connect.Request[struct{}] }

func (c clientSpec) Spec() connect.Spec { return connect.Spec{IsClient: true} }
