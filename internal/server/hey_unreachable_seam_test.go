package server

import (
	"net/http"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/plugintest/heyfake"
)

// One HEY box the CLI cannot read costs nothing else (docs/plugin-standard.md
// rule 7). Every email is a tile in everything, so everything refused for one
// box was a 404 for every email the content door served, in every box; now
// everything lists what the plugin remembers of every box, every email's page
// serves, and the plugin's health carries the box's reason.
func TestHeyOneFailingBoxLeavesEveryEmailServing(t *testing.T) {
	hey := heyAccount(t)
	hey.FailBox("feedbox", heyfake.ExitNotFound, "no box of kind feedbox")
	hs, cl, info := heyStack(t, hey)
	all, err := cl.GetGrid(t.Context(), &gridwellv1.GetGridRequest{GridId: heyGrids(t, info)["everything"]})
	if err != nil {
		t.Fatalf("one failing box refused everything: %v", err)
	}
	for _, subject := range []string{"Lunch plans", "Invoice 41", "Conference talk", "Lease renewal"} {
		email := tileWithLabel(t, all, subject)
		res, body := get(t, hs.Client(), rpc.PageURL(hs.URL, ContentToken(testPassword), heyNS+"/"+email.Id), "")
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d %.120q", subject, res.StatusCode, body)
		}
	}
	if h := sourceHealth(t, cl); h.GetHealthy() || !strings.Contains(h.GetDetail(), "no box of kind feedbox") {
		t.Errorf("health = %+v, want the failing box's reason", h)
	}
}

// A CLI signed out after Info is no verdict that blanks a grid (decision 3,
// 2026-10-03): the Imbox keeps every row it remembers and the health says
// why, and signing back in clears it.
func TestHeySignedOutKeepsTheGridAndSaysWhy(t *testing.T) {
	hey := heyAccount(t)
	// A refresh this short walks on every read, so the read after the sign-out
	// is the one that learns it.
	cl := newPluginClient(t, "hey", hey.Config(map[string]string{"refresh": "1ms"}))
	info, err := cl.Info(t.Context(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	imbox := heyGrids(t, info)["imbox"]
	awaitGrid(t, cl, imbox, func(g *gridwellv1.GetGridResponse) bool { return len(g.Tiles) == 2 })

	for _, box := range []string{"imbox", "laterbox", "asidebox", "feedbox", "trailbox", "bubblebox"} {
		hey.FailBox(box, heyfake.ExitAuthRequired, "Not logged in")
	}
	var h *gridwellv1.EventPluginHealth
	out := awaitGrid(t, cl, imbox, func(*gridwellv1.GetGridResponse) bool {
		h = sourceHealth(t, cl)
		return !h.GetHealthy()
	})
	if !strings.Contains(h.GetDetail(), "Not logged in") {
		t.Errorf("health = %+v, want the CLI's reason", h)
	}
	if len(out.Tiles) != 2 {
		t.Errorf("signed out, the imbox = %v, want both remembered rows", out.Tiles)
	}

	for _, box := range []string{"imbox", "laterbox", "asidebox", "feedbox", "trailbox", "bubblebox"} {
		hey.HealBox(box)
	}
	awaitGrid(t, cl, imbox, func(*gridwellv1.GetGridResponse) bool { return sourceHealth(t, cl).GetHealthy() })
}
