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
// dark, not a verdict (docs/plugin-standard.md rule 3): the Imbox keeps the
// row the user placed and the source's health says it is not answering, and
// putting the CLI back brings the whole box back with no restart. A verdict
// here would refuse the grid outright, and a placed email would vanish until
// the CLI returned.
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
	dark := awaitGrid(t, cl, imbox, func(g *gridwellv1.GetGridResponse) bool { return len(g.Tiles) == 1 })
	if got := dark.Tiles[0]; got.Id != placed.Tile.Id || got.X != 7 || got.Y != 3 {
		t.Errorf("the placed row drifted in the dark: %+v", got)
	}
	if h := sourceHealth(t, cl); h.GetHealthy() || !strings.Contains(h.GetDetail(), "source is not answering") {
		t.Errorf("health = %+v, want the source named as not answering", h)
	}

	hey.Restore(t)
	awaitGrid(t, cl, imbox, func(g *gridwellv1.GetGridResponse) bool { return len(g.Tiles) == 2 })
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

// sourceHealth is the health a subscriber arriving now is told.
func sourceHealth(t *testing.T, cl namespace.Namespace) *gridwellv1.EventPluginHealth {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var h *gridwellv1.EventPluginHealth
	got := errors.New("got")
	err := cl.Subscribe(ctx, &gridwellv1.SubscribeRequest{}, func(ev *gridwellv1.Event) error {
		if h = ev.GetPluginHealth(); h != nil {
			return got
		}
		return nil
	})
	if !errors.Is(err, got) {
		t.Fatalf("no health arrived: %v", err)
	}
	return h
}
