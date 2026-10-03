package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// A hey CLI that goes missing after the plugin's Info passed is a source gone
// dark, not a verdict (docs/plugin-standard.md rules 3 and 7): the Imbox keeps
// every row it remembers, the placed one where the user left it, and the
// source's health says why, and putting the CLI back clears it with no
// restart. A verdict here would refuse the grid outright, and a placed email
// would vanish until the CLI returned.
func TestHeyCLIGoneAfterInfoReadsDark(t *testing.T) {
	hey := heyAccount(t)
	// A refresh this short walks on every read, so the read after the CLI goes
	// missing is the one that learns it.
	cl := newPluginClient(t, "hey", hey.Config(map[string]string{"refresh": "1ms"}))
	info, err := cl.Info(t.Context(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	imbox := heyGrids(t, info)["imbox"]
	g, err := cl.GetGrid(t.Context(), &gridwellv1.GetGridRequest{GridId: imbox})
	if err != nil {
		t.Fatal(err)
	}
	lunch := tileWithLabel(t, g, "Lunch plans")
	placed, err := cl.PlaceTile(t.Context(), &gridwellv1.PlaceTileRequest{TileId: lunch.Id, GridId: imbox, X: 7, Y: 3, W: 2, H: 2})
	if err != nil {
		t.Fatal(err)
	}

	hey.Remove(t)
	var h *gridwellv1.EventPluginHealth
	dark := awaitGrid(t, cl, imbox, func(g *gridwellv1.GetGridResponse) bool {
		h = sourceHealth(t, cl)
		return !h.GetHealthy()
	})
	if !strings.Contains(h.GetDetail(), "could not be run") {
		t.Errorf("health = %+v, want the reason the CLI could not be read", h)
	}
	if len(dark.Tiles) != 2 {
		t.Fatalf("the dark imbox = %v, want every remembered row", dark.Tiles)
	}
	got := tileWithLabel(t, dark, "Lunch plans")
	if got.Id != placed.Tile.Id || got.X != 7 || got.Y != 3 {
		t.Errorf("the placed row drifted in the dark: %+v", got)
	}

	hey.Restore(t)
	awaitGrid(t, cl, imbox, func(*gridwellv1.GetGridResponse) bool { return sourceHealth(t, cl).GetHealthy() })
}

// awaitGrid reads grid until done says so. Every read must succeed: a refusal
// is the verdict this file exists to rule out.
func awaitGrid(t *testing.T, cl namespace.Namespace, grid string, done func(*gridwellv1.GetGridResponse) bool) *gridwellv1.GetGridResponse {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		g, err := cl.GetGrid(t.Context(), &gridwellv1.GetGridRequest{GridId: grid})
		if err != nil {
			t.Fatalf("the grid refused: %v", err)
		}
		if done(g) {
			return g
		}
		if time.Now().After(deadline) {
			t.Fatalf("the grid never settled: %v", g.Tiles)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sourceHealth is the health a subscriber arriving now is told. A healthy
// source tells a new subscriber nothing, so a short silence is healthy.
func sourceHealth(t *testing.T, cl namespace.Namespace) *gridwellv1.EventPluginHealth {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	h := &gridwellv1.EventPluginHealth{Healthy: true}
	got := errors.New("got")
	err := cl.Subscribe(ctx, &gridwellv1.SubscribeRequest{}, func(ev *gridwellv1.Event) error {
		if told := ev.GetPluginHealth(); told != nil {
			h = told
			return got
		}
		return nil
	})
	if err != nil && !errors.Is(err, got) && ctx.Err() == nil {
		t.Fatalf("subscribe: %v", err)
	}
	return h
}
