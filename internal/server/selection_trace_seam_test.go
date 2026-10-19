package server_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
	"github.com/josephburnett/gridwell/internal/trace"
)

// selectRefuser is a namespace whose every select is refused with why.
type selectRefuser struct {
	subscriptionSink
	why string
}

func (s *selectRefuser) SetSelection(context.Context, *gridwellv1.SetSelectionRequest) (*gridwellv1.SetSelectionResponse, error) {
	return nil, errors.New(s.why)
}

// The all-clear tries every namespace, so every one that refuses is in the
// answer and in the trace, not only the first: a second refusal dropped is
// a selection the user thinks is cleared and is not.
func TestTheAllClearAnswersEveryNamespacesRefusal(t *testing.T) {
	const a, b = "pinsa01", "pinsb01"
	reg := plugin.NewRegistry()
	reg.Register(a, "feed", &selectRefuser{why: "first source refused"}, nil)
	reg.Register(b, "feed", &selectRefuser{why: "second source refused"}, nil)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	err := cl.SetSelection(context.Background(), &gridwellv1.SetSelectionRequest{Clear: true, All: true})
	if err == nil || !strings.Contains(err.Error(), "first source refused") || !strings.Contains(err.Error(), "second source refused") {
		t.Fatalf("the all-clear answered %v, want both refusals", err)
	}
	seen := map[string]bool{}
	var span map[string]string
	for _, rec := range trace.Default().Snapshot() {
		if rec.Src == "selection" && rec.Kind == "clear" && strings.Contains(rec.Msg, "source refused") {
			seen[rec.KV["ns"]] = true
		}
		if rec.Src == "router" && strings.HasPrefix(rec.Msg, "SetSelection error:") && strings.Contains(rec.Msg, "second source refused") {
			span = rec.KV
		}
	}
	if !seen[a] || !seen[b] {
		t.Errorf("selection/clear records name %v, want both %s and %s", seen, a, b)
	}
	if span["all"] != "true" || span["clear"] != "true" || span["recurse"] != "false" || span["add"] != "0" || span["remove"] != "0" {
		t.Errorf("the SetSelection span = %v, want its flags and counts", span)
	}
}
