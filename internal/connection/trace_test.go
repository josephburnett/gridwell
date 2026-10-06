package connection

import (
	"context"
	"errors"
	"reflect"
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
	st := openStore(t)
	dialer := func(dial.Config) (namespace.Namespace, func(), error) {
		return nil, nil, errors.New("trace-dial-refused")
	}
	s, err := New(st, dialer, "", []config.ConnectionConfig{{Name: "tracedial", Addr: "/nowhere"}}, nil)
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

// The transport writes its rows through the node store's one write funnel, so
// boot's reconcile and a learned landing leave the store's write records
// naming each row, and a boot that changes nothing leaves none.
func TestConnectionRowWritesAreTheStoresTracedWrites(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	conns := []config.ConnectionConfig{{Name: "tracerow", Addr: "/s"}}
	writes := func(after uint64) map[string]bool {
		got := map[string]bool{}
		for _, rec := range trace.Default().Snapshot() {
			if rec.Seq > after && rec.Src == "store" && rec.Kind == "write" &&
				(rec.KV["keys"] == "conn/tracerow" || rec.KV["keys"] == "conn/traceretired") {
				got[rec.Msg+" "+rec.KV["keys"]] = true
			}
		}
		return got
	}
	mark := func() uint64 {
		recs := trace.Default().Snapshot()
		if len(recs) == 0 {
			return 0
		}
		return recs[len(recs)-1].Seq
	}

	before := mark()
	s, err := New(st, landingDialer("rnode1/7"), "", conns, []string{"traceretired"})
	if err != nil {
		t.Fatal(err)
	}
	s.ConnectAll(ctx)
	_ = s.Close()
	want := map[string]bool{
		"DeclareConnection conn/tracerow":    true,
		"RetireConnection conn/traceretired": true,
		"SetConnectionRoot conn/tracerow":    true,
	}
	if got := writes(before); !reflect.DeepEqual(got, want) {
		t.Errorf("first boot's store writes = %v, want %v", got, want)
	}

	before = mark()
	s, err = New(st, landingDialer("rnode1/7"), "", conns, []string{"traceretired"})
	if err != nil {
		t.Fatal(err)
	}
	s.ConnectAll(ctx)
	_ = s.Close()
	if got := writes(before); len(got) != 0 {
		t.Errorf("a boot that changes nothing wrote %v", got)
	}
}
