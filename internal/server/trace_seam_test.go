package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/trace"
)

// The whole seam in one file: the client's own records come in the door, an
// rpc crosses the codec carrying the same request id, and the dump holds all
// three in one seq order. A unit test on either side of that door would pass
// with the request id dropped in the middle.
func TestADumpHoldsTheClientsRecordAndTheRPCItCaused(t *testing.T) {
	home := t.TempDir()
	hs := serveWeb(t, mustNew(t, plugin.NewRegistry(), Config{Home: home}))
	const reqID = "k3f9x2a"
	marker := "gesture " + reqID

	body := `{"origin":"client","src":"nav","kind":"nav","msg":"` + marker +
		`","kv":{"req":"` + reqID + `"},"cid":"c1","ct":11}` + "\n"
	res, err := hs.Client().Post(hs.URL+tracewire.Path, "application/x-ndjson", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /trace = %d, want 204", res.StatusCode)
	}

	cl := gridwellv1connect.NewGridwellClient(hs.Client(), hs.URL)
	req := connect.NewRequest(&pb.HandshakeRequest{})
	req.Header().Set(tracewire.RequestHeader, reqID)
	if _, err := cl.Handshake(context.Background(), req); err != nil {
		t.Fatalf("Handshake: %v", err)
	}

	res, err = hs.Client().Post(hs.URL+tracewire.DumpPath, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	var client, start, end tracewire.Record
	var lastSeq uint64
	for _, rec := range readDump(t, out.Path) {
		if rec.Seq <= lastSeq {
			t.Fatalf("the dump is out of order: seq %d after %d", rec.Seq, lastSeq)
		}
		lastSeq = rec.Seq
		switch {
		case rec.Msg == marker:
			client = rec
		case rec.Src == "router" && rec.KV["req"] == reqID && rec.Msg == "Handshake start":
			start = rec
		case rec.Src == "router" && rec.KV["req"] == reqID && strings.HasPrefix(rec.Msg, "Handshake ok"):
			end = rec
		}
	}
	if client.Seq == 0 {
		t.Error("the client's record is not in the dump")
	}
	if start.Seq == 0 || end.Seq == 0 {
		t.Fatalf("the router's rpc records are not both in the dump under req %q (start=%+v end=%+v)", reqID, start, end)
	}
	if client.Origin != tracewire.OriginClient || client.CID != "c1" || client.CT != 11 {
		t.Errorf("the client's record came back as %+v", client)
	}
	if start.Kind != "rpc" || end.Kind != "rpc" {
		t.Errorf("the rpc records are kinds %q and %q, want rpc", start.Kind, end.Kind)
	}
	if client.Seq >= start.Seq || start.Seq >= end.Seq {
		t.Errorf("seq is the one total order, and it does not hold: client %d, start %d, end %d",
			client.Seq, start.Seq, end.Seq)
	}
	if _, ok := end.KV["ms"]; !ok {
		t.Errorf("the exit record carries no duration: %+v", end)
	}
	if end.Origin != tracewire.OriginNode || start.Origin != tracewire.OriginNode {
		t.Errorf("an rpc record is not the node's: %+v %+v", start, end)
	}
}

// An rpc nobody tagged still says what it did; the join key is simply empty.
func TestAnRPCWithoutARequestIDIsStillTraced(t *testing.T) {
	hs := serveWeb(t, mustNew(t, plugin.NewRegistry(), Config{Home: t.TempDir()}))
	cl := gridwellv1connect.NewGridwellClient(hs.Client(), hs.URL)
	if _, err := cl.GetGrid(context.Background(), connect.NewRequest(&pb.GetGridRequest{GridId: "nope/1"})); err == nil {
		t.Fatal("GetGrid on an unrouted id answered")
	}
	var errored bool
	for _, rec := range trace.Default().Snapshot() {
		if rec.Src == "router" && strings.HasPrefix(rec.Msg, "GetGrid error:") && rec.KV["code"] != "" {
			errored = true
		}
	}
	if !errored {
		t.Error("a failed rpc left no record carrying its code")
	}
}

// A streaming verb is one span around the whole stream, not none.
func TestAStreamingRPCGetsBothRecords(t *testing.T) {
	hs := serveWeb(t, mustNew(t, plugin.NewRegistry(), Config{Home: t.TempDir()}))
	cl := gridwellv1connect.NewGridwellClient(hs.Client(), hs.URL)
	const reqID = "stream-req"
	req := connect.NewRequest(&pb.ReadContentRequest{TileId: "nope/1"})
	req.Header().Set(tracewire.RequestHeader, reqID)
	stream, err := cl.ReadContent(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
	}
	if stream.Err() == nil {
		t.Fatal("ReadContent on an unrouted id answered")
	}
	var started, ended bool
	for _, rec := range trace.Default().Snapshot() {
		if rec.Src != "router" || rec.KV["req"] != reqID {
			continue
		}
		started = started || rec.Msg == "ReadContent start"
		ended = ended || strings.HasPrefix(rec.Msg, "ReadContent error:")
	}
	if !started || !ended {
		t.Errorf("the stream left start=%v end=%v; a streaming verb gets both", started, ended)
	}
}

// The connection door is the other codec over the same router, and it traces
// too: a node debugging a federated gesture has the far hop in its own ring.
func TestTheConnectionDoorTracesItsRPCs(t *testing.T) {
	srv := mustNew(t, plugin.NewRegistry(), Config{Home: t.TempDir()})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := ConnectionDoorServer(srv.ConnectionHandler())
	go hs.Serve(ln)
	t.Cleanup(func() { hs.Close() })

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	const reqID = "conn-door-req"
	ctx := metadata.AppendToOutgoingContext(context.Background(), tracewire.RequestHeader, reqID)
	if _, err := pb.NewGridwellClient(conn).Info(ctx, &pb.InfoRequest{}); err != nil {
		t.Fatalf("Info on the connection door: %v", err)
	}
	var started, ended bool
	for _, rec := range trace.Default().Snapshot() {
		if rec.Src != "router" || rec.KV["req"] != reqID {
			continue
		}
		started = started || rec.Msg == "Info start"
		ended = ended || rec.Msg == "Info ok"
	}
	if !started || !ended {
		t.Errorf("the connection door left start=%v end=%v under req %q", started, ended, reqID)
	}
}
