package dial

// The unanswered-open seam: x/crypto's channel open waits for the peer's
// answer with no bound of its own, and a session that dies under the open
// never sends one. gRPC bounds one connect attempt by the context it hands
// the dialer, so the dialer must end with it, or the connection parks in
// CONNECTING for good and every fail-fast RPC with no deadline waits on it.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/internal/connection/dial/dialtest"
)

func TestADialWhoseChannelOpenIsNeverAnsweredEndsWithItsContext(t *testing.T) {
	creds, sshd := dialtest.Restartable(t, t.TempDir())
	r, err := newRedialer(Config{Host: creds.Addr, User: "joe", KeyPath: creds.KeyPath,
		KnownHosts: creds.KnownHostsPath, Addr: "/nowhere/federation.sock"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.close)
	sshd.Silence()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, err := r.dial(ctx, "/nowhere/federation.sock")
		if conn != nil {
			conn.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("dial ended with %v, want its context's deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dial still parked on an unanswered channel open 5s after its context ended")
	}
	// The session that left the open unanswered is not reused.
	if r.current() != nil {
		t.Fatal("the session that never answered is still the current one")
	}
}
