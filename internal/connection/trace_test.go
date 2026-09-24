package connection

import (
	"errors"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/trace"
)

// A connection that will not answer is a runtime state, and the record is
// where the reason and the moment are kept: the strip shows the transition,
// nothing shows the attempt.
func TestADialAndItsFailureAreBothTraced(t *testing.T) {
	db := openConnDB(t)
	dialer := func(dial.Config) (namespace.Namespace, func(), error) {
		return nil, nil, errors.New("trace-dial-refused")
	}
	s, err := New(db, dialer, "", []config.ConnectionConfig{{Name: "tracedial", Addr: "/nowhere"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ensureLive(s.conns["tracedial"]); err == nil {
		t.Fatal("a refused dial answered")
	}
	var dialed, failed, down bool
	for _, rec := range trace.Default().Snapshot() {
		if rec.Src != "connection" || rec.KV["conn"] != "tracedial" {
			continue
		}
		dialed = dialed || rec.Msg == "dial"
		failed = failed || strings.Contains(rec.Msg, "trace-dial-refused") && rec.Kind == "dial"
		down = down || strings.HasPrefix(rec.Msg, "down: ") && rec.Kind == "health"
	}
	if !dialed || !failed || !down {
		t.Errorf("dial=%v failed=%v down=%v; want the attempt, its reason and the transition", dialed, failed, down)
	}
}
