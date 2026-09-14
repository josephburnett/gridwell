//go:build js && wasm

package main

// The + menu belongs to the node a pane is inside. A context is one node's
// plugin list plus its shells flag, keyed by the pane's grid's node_ns ("" is
// this node, the boot handshake). Remote contexts are fetched through the
// routed Handshake and cached for the session.

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
)

// menuContext is one node's menu: its plugins and its shell policy.
type menuContext struct {
	plugins        []*gridwellv1.PluginInfo
	shellsDisabled bool
	// fetched marks a completed load. Concurrent opens are kept to one read
	// by a.fetch.menuFetch, not by a flag here, so the read is bounded and a
	// Handshake the network swallows does not leave the menu without its
	// plugin section for the life of the page.
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
// remote context not yet loaded. The "" context is the boot handshake, always
// present.
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
		if ctx, done, ok := a.fetch.menuFetch.Begin(ns); ok {
			go a.fetchMenuCtx(ctx, done, ns)
		}
	}
	return mc
}

// fetchMenuCtx loads one remote node's menu on the claim menuCtx opened for
// it. A failure leaves the context unfetched and surfaces. Nothing else
// retries: every draw of the open menu asks again, which is the retry.
func (a *App) fetchMenuCtx(ctx context.Context, done func() bool, ns string) {
	defer done()
	lp, err := a.cl.HandshakeNS(ctx, ns)
	if err != nil {
		// reportErr schedules a frame, so the next draw finds no claim and
		// no context and starts a fresh read.
		a.surfaceRPCError("Handshake", err)
		return
	}
	mc := a.views.menuCtxs[ns]
	mc.plugins = rpc.MenuRows(lp)
	mc.shellsDisabled = lp.ShellsDisabled
	mc.fetched = true
	a.draw()
}
