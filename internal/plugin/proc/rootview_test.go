package proc

import (
	"context"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// proc's half of the root-view round trip (framing audit 2026-08-13) —
// same contract as fs: zero zoom until set, exact values after.
func TestProcRootViewRoundTrip(t *testing.T) {
	p, err := Open(filepath.Join(t.TempDir(), "store.db"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	ctx := context.Background()
	info, err := p.Info(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if info.RootViewZoom != 0 {
		t.Fatalf("fresh zoom = %v, want 0", info.RootViewZoom)
	}
	if _, err := p.SetRootView(ctx, &gridwellv1.SetRootViewRequest{
		RootGridId: info.RootGridId, Cx: 1, Cy: 2, Zoom: 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	again, _ := p.Info(ctx, nil)
	if again.RootViewCx != 1 || again.RootViewCy != 2 || again.RootViewZoom != 0.5 {
		t.Errorf("root view = (%v,%v,%v), want (1,2,0.5)",
			again.RootViewCx, again.RootViewCy, again.RootViewZoom)
	}
}
