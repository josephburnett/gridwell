package connection

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/internal/config"
)

// A connection whose host-local config stops resolving after boot, its key
// file gone, will not dial: dark, its reads served from cache, never a
// stale claim.
func TestAConfigThatStopsResolvingIsUnheard(t *testing.T) {
	dir := t.TempDir()
	key, known := filepath.Join(dir, "id"), filepath.Join(dir, "known_hosts")
	for _, f := range []string{key, known} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(openStore(t), landingDialer("rnode1/7"), "", []config.ConnectionConfig{{
		Name: "rtb", Host: "rtb.example", User: "joe", Key: key, KnownHosts: known, Addr: "/s"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: "rtb/rnode1/7"})
	if !gwerr.IsUnheard(err) || gwerr.IsConflict(err) {
		t.Fatalf("a read through a connection whose key is gone = %v, want unheard, not a conflict", err)
	}
}
