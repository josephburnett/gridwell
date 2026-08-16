package fs_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/api/compose"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/plugins/fs"
)

// The fs root's persisted viewport (framing audit 2026-08-13): SetRootView
// used to be silently swallowed — pan an fs root, gone on re-entry. The
// round trip crosses the real gRPC seam, and a fresh Info (no write yet)
// reports zero zoom so the client can tell "never set" from any framing.
func TestFSRootViewRoundTripOverGRPC(t *testing.T) {
	dir := t.TempDir()
	p, err := fs.Open(filepath.Join(t.TempDir(), "store.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	p.SetRoot(dir)
	client, closer, err := compose.ServeInProcess(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	ctx := context.Background()

	info, err := client.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.RootViewZoom != 0 {
		t.Fatalf("fresh root view zoom = %v, want 0 (never set)", info.RootViewZoom)
	}

	if _, err := client.SetRootView(ctx, &gridwellv1.SetRootViewRequest{
		RootGridId: info.RootGridId, Cx: 3, Cy: -2, Zoom: 0.25,
	}); err != nil {
		t.Fatalf("SetRootView: %v", err)
	}
	again, err := client.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if again.RootViewCx != 3 || again.RootViewCy != -2 || again.RootViewZoom != 0.25 {
		t.Errorf("root view = (%v,%v,%v), want (3,-2,0.25)",
			again.RootViewCx, again.RootViewCy, again.RootViewZoom)
	}
}
