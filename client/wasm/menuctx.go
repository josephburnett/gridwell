//go:build js && wasm

package main

// The + menu belongs to the node a pane is inside. A context is one node's
// plugin list plus its shells flag, keyed by the pane's grid's node_ns ("" is
// this node, the boot handshake). Remote contexts are fetched through the
// routed Handshake, and every context is asked again on a health transition
// (refetchMenus).

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/pane"
)

// menuContext is one node's menu: its plugins and its shell policy.
type menuContext struct {
	plugins        []*gridwellv1.PluginInfo
	shellsDisabled bool
	// fetched marks a completed load. Concurrent opens are kept to one read,
	// and a failed one from being asked every frame, by a.fetch.menus, not by
	// a flag here, so the read is bounded and a Handshake the network
	// swallows does not leave the menu without its plugin section for the
	// life of the page.
	fetched bool
}

// paneNodeNS returns the menu-context key: the namespace chain of the node
// serving pane p's current grid. "" for the local node and for an uncached
// grid, where the local list is the least-wrong face.
func (a *App) paneNodeNS(p *pane.Pane) string {
	return a.gridNodeNS(a.gridIDForPane(p))
}

// gridNodeNS is paneNodeNS by grid id. A drop resolves its destination grid
// rather than a pane's leaf grid, since the two differ when the cursor
// promoted into an open well.
func (a *App) gridNodeNS(gridID string) string {
	if g, ok := a.c.Grid(gridID); ok {
		return g.Meta.NodeNs
	}
	return ""
}

// menuCtx returns the context for pane p, kicking a background fetch for a
// remote context not yet loaded. The "" context is a.plugins, always present.
func (a *App) menuCtx(p *pane.Pane) *menuContext {
	ns := a.paneNodeNS(p)
	if ns == "" {
		return &menuContext{plugins: a.plugins, shellsDisabled: !a.caps.Shells, fetched: true}
	}
	mc, ok := a.views.menuCtxs[ns]
	if !ok {
		mc = &menuContext{}
		a.views.menuCtxs[ns] = mc
	}
	if !mc.fetched {
		if ctx, done, ok := a.fetch.menus.Ask(ns); ok {
			go a.fetchMenuCtx(ctx, done, ns)
		}
	}
	return mc
}

// fetchMenuCtx loads one remote node's menu on the claim menuCtx opened for
// it. A failure leaves the context unfetched, surfaces, and latches, so the
// draw of the open menu asks again only when inflight.Reads clears it. A menu
// has no dead face, so only the client's own cancel goes unsaid.
func (a *App) fetchMenuCtx(ctx context.Context, done func() bool, ns string) {
	defer done()
	lp, err := a.cl.HandshakeNS(ctx, ns)
	o := clientsync.Of(err)
	a.fetch.menus.Settle(ns, clientsync.ReactRead(o))
	if err != nil {
		if o != clientsync.OutcomeAbandoned {
			a.surfaceRPCError("Handshake", err)
		}
		return
	}
	mc := a.views.menuCtxs[ns]
	mc.plugins = lp.Plugins
	mc.shellsDisabled = lp.ShellsDisabled
	mc.fetched = true
	a.draw()
}

// refetchMenus asks every node's plugin list again after a health transition
// or a stream gap. A transition changes a row of the menu that lists the
// source, which is the node above it rather than one the source reaches, so
// no menu is left out: a plugin whose refusal ended gets its entries back,
// and one that began refusing loses them. The local list is asked now, since
// dead-link verdicts read it; a remote one on its next draw.
func (a *App) refetchMenus() {
	for _, mc := range a.views.menuCtxs {
		mc.fetched = false
	}
	a.fetch.menus.Change("")
	a.askLocalMenu()
}

func (a *App) askLocalMenu() {
	if ctx, done, ok := a.fetch.menus.Ask(""); ok {
		go a.fetchLocalMenu(ctx, done)
	}
}

// fetchLocalMenu re-reads this node's plugin list. The rest of the boot
// handshake (caps, the content token, home) is immutable and stays. An ask
// refused while this one was in flight came from a newer transition, so it is
// asked again.
func (a *App) fetchLocalMenu(ctx context.Context, done func() bool) {
	lp, err := a.cl.Handshake(ctx)
	owed := done()
	o := clientsync.Of(err)
	a.fetch.menus.Settle("", clientsync.ReactRead(o))
	switch {
	case o == clientsync.OutcomeAbandoned:
	case err != nil:
		a.surfaceRPCError("Handshake", err)
	default:
		a.plugins = lp.Plugins
		a.draw()
	}
	if owed {
		a.askLocalMenu()
	}
}
