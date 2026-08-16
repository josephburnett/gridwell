//go:build js && wasm

package main

// The MENU CONTEXT (remote-menu, 2026-08-16): the + menu belongs to the
// NODE a pane is inside — "when I descend into a node, I am there". A
// context is one node's plugin list + its shells flag, keyed by the
// pane's grid's node_ns ("" = this node, the boot handshake). Remote
// contexts are fetched through the routed ListPlugins (ids arrive
// re-qualified for this receiver) and cached for the session; the
// mountcache makes the fetch answer even while the mount is dark.

import (
	"context"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
)

// menuContext is one node's menu: its plugins and its shell policy.
type menuContext struct {
	plugins        []rpc.PluginInfo
	shellsDisabled bool
	// fetched marks a completed load; inflight dedups concurrent opens.
	fetched  bool
	inflight bool
}

// paneNodeNS returns the namespace chain of the node serving pane p's
// current grid — the menu-context key. "" for the local node AND for an
// uncached grid (honest: until the grid loads, nothing about the pane is
// renderable anyway, primitives are already hidden by the writable gate,
// and the local list is the least-wrong face).
func (a *App) paneNodeNS(p *pane.Pane) string {
	if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
		return g.Meta.NodeNS
	}
	return ""
}

// menuCtx returns the context for pane p, kicking a background fetch for
// a remote context not yet loaded (the menu redraws when it lands). The
// "" context is the boot handshake — always present, never fetched here.
func (a *App) menuCtx(p *pane.Pane) *menuContext {
	ns := a.paneNodeNS(p)
	if ns == "" {
		return &menuContext{plugins: a.plugins, shellsDisabled: !a.caps.Shells, fetched: true}
	}
	ctx, ok := a.menuCtxs[ns]
	if !ok {
		ctx = &menuContext{}
		a.menuCtxs[ns] = ctx
	}
	if !ctx.fetched && !ctx.inflight {
		ctx.inflight = true
		go a.fetchMenuCtx(ns)
	}
	return ctx
}

// fetchMenuCtx loads one remote node's menu through the routed
// ListPlugins. A transport failure leaves the context unfetched so the
// next open retries (and surfaces once); the mount's health notice is
// already the ambient signal.
func (a *App) fetchMenuCtx(ns string) {
	lp, err := a.cl.ListPluginsNS(context.Background(), ns)
	ctx := a.menuCtxs[ns]
	ctx.inflight = false
	if err != nil {
		a.surfaceRPCError("ListPlugins", err)
		return
	}
	ctx.plugins = lp.Plugins
	ctx.shellsDisabled = lp.ShellsDisabled
	ctx.fetched = true
	a.draw()
}
