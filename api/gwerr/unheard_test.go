package gwerr

import (
	"context"
	"io"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// What the dial hands back says whether the far node spoke, and nothing else
// about the error changes: its code, its words and its verdict details.
func TestReceivedTellsAnAnswerFromSilence(t *testing.T) {
	answered := Answered(DeadLink("", "gone"))
	for _, tc := range []struct {
		name    string
		err     error
		unheard bool
		code    codes.Code
	}{
		{"an answered Unavailable is heard", Received(Answered(status.Error(codes.Unavailable, "dns"))), false, codes.Unavailable},
		{"an answered verdict keeps its detail", Received(answered), false, codes.NotFound},
		{"an unstamped Unavailable is unheard", Received(status.Error(codes.Unavailable, "tunnel down")), true, codes.Unavailable},
		{"an unstamped verdict is unheard, its code kept", Received(DeadLink("", "gone")), true, codes.NotFound},
		{"the transport's own", Unheard("shutting down"), true, codes.Unavailable},
		{"a context error answered keeps its code", Answered(context.Canceled), false, codes.Canceled},
		// The next hop heard this node, whoever this node could not hear.
		{"an unheard relayed is answered", Received(Answered(Unheard("third node down"))), false, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUnheard(tc.err); got != tc.unheard {
				t.Errorf("IsUnheard = %v, want %v", got, tc.unheard)
			}
			if got := status.Code(tc.err); got != tc.code {
				t.Errorf("code = %v, want %v", got, tc.code)
			}
		})
	}
	if !IsDeadLink(Received(answered)) || !IsDeadLink(Received(DeadLink("", "gone"))) {
		t.Error("the dial's verdict hid the dead link")
	}
	if got := status.Convert(Received(status.Error(codes.Unavailable, "tunnel down"))).Message(); got != "tunnel down" {
		t.Errorf("message = %q, want the far side's words", got)
	}
	if Received(nil) != nil || Received(io.EOF) != io.EOF || Answered(nil) != nil {
		t.Error("an end of stream or no error came back changed")
	}
}
