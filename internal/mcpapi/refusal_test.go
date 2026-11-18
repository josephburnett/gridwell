package mcpapi

import (
	"testing"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/josephburnett/gridwell/api/gwerr"
)

// A tool error says the MCP's words and keeps the router's code under them,
// so a reader of the code (the place API's status) reads the router's.
func TestARefusalKeepsTheRoutersCode(t *testing.T) {
	for _, c := range []struct {
		err   error
		words string
	}{
		{status.Error(gcodes.NotFound, "no tile 7"), "not found: no tile 7"},
		{status.Error(gcodes.PermissionDenied, "mode 000"), "PermissionDenied: mode 000"},
		{gwerr.DeadLink("lnode1/nope", "nope is not declared"), "dead link: nope is not declared"},
	} {
		got := refusal(c.err)
		if got.Error() != c.words {
			t.Errorf("refusal(%v) says %q, want %q", c.err, got, c.words)
		}
		if status.Code(got) != status.Code(c.err) {
			t.Errorf("refusal(%v) carries %v, want %v", c.err, status.Code(got), status.Code(c.err))
		}
	}
}
