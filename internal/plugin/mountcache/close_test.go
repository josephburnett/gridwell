package mountcache

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// gated is an upstream whose GetGrid parks the caller until release is
// closed — the walk held mid-flight, deterministically, ignoring ctx the
// way a slow network read does.
type gated struct {
	pb.GridwellClient
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (g *gated) GetGrid(ctx context.Context, in *pb.GetGridRequest, opts ...grpc.CallOption) (*pb.GetGridResponse, error) {
	g.once.Do(func() { close(g.entered) })
	<-g.release
	return g.GridwellClient.GetGrid(ctx, in, opts...)
}

// Closing the cache mid-walk: the closer must cancel AND wait for the
// walk before closing the DB. Before, it only cancelled — the walk's next
// store hit the closed DB and every shutdown during a prefetch logged
// "cache degraded" for a cache that was fine.
func TestCloseWaitsForTheWalk(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	raw := serveInProcess(t, local.New(st, nil))
	g := &gated{GridwellClient: raw, entered: make(chan struct{}), release: make(chan struct{})}
	cc, closer, err := Open(&darkable{GridwellClient: g}, filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(nil) })

	if _, err := cc.Subscribe(context.Background(), &pb.SubscribeRequest{}); err != nil {
		t.Fatal(err)
	}
	<-g.entered // the walk is inside GetGrid, its store of that grid still ahead
	closed := make(chan struct{})
	go func() { closer(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("closer returned while the prefetch walk was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(g.release)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("closer never returned after the walk was released")
	}
	if strings.Contains(logs.String(), "cache degraded") {
		t.Fatalf("the walk wrote into the closed cache:\n%s", logs.String())
	}
}
