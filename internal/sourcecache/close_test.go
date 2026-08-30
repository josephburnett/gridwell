package sourcecache

import (
	"bytes"
	"context"
	"github.com/josephburnett/gridwell/internal/namespace"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// gated is an upstream whose GetGrid parks the caller until release is closed:
// the walk held mid-flight, deterministically, ignoring ctx the way a slow
// network read does.
type gated struct {
	namespace.Namespace
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (g *gated) GetGrid(ctx context.Context, in *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	g.once.Do(func() { close(g.entered) })
	<-g.release
	return g.Namespace.GetGrid(ctx, in)
}

// Closing the cache mid-walk: the closer must cancel and wait for the walk
// before closing the DB. Cancelling alone leaves the walk's next store hitting
// a closed DB, so every shutdown during a prefetch logs "cache degraded" for a
// cache that was fine.
func TestCloseWaitsForTheWalk(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	g := &gated{Namespace: local.New(st, nil), entered: make(chan struct{}), release: make(chan struct{})}
	cache, err := Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	cc := cache.Front(&darkable{Namespace: g}, Options{Prefetch: true})
	closer := func() { _ = cache.Close() }

	var logs bytes.Buffer
	log.SetOutput(&logs)
	// Restore a real writer, never nil: the standard logger is process-wide,
	// and another test's prefetch goroutine can still be walking when this
	// cleanup runs. log.Printf into a nil writer panics and takes the whole
	// package's test binary with it.
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	go func() {
		_ = cc.Subscribe(subCtx, &pb.SubscribeRequest{}, func(*pb.Event) error { return nil })
	}()
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
