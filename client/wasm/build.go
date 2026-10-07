//go:build js && wasm

package main

// The page's one answer to the door refusing its build, wherever the refusal
// arrived: an rpc, the event stream, a shell socket. nodebuild.Decide is the
// rule; this runs its arms.

import (
	"syscall/js"
	"time"

	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/nodebuild"
	"github.com/josephburnett/gridwell/client/traceevent"
)

// staleBuild acts once per verdict: every refused call reports here, and a
// page holding unsaved text keeps being refused until it holds none.
func (a *App) staleBuild(node string) {
	v := nodebuild.Decide(pageReloaded(), a.gate.Accepted(), len(a.c.DirtyTileIDs()))
	if a.stale != nil && *a.stale == v {
		return
	}
	a.stale = &v
	a.emit(traceevent.StaleBuild(a.gate.Build(), node, v.String()))
	if v != nodebuild.Reload {
		a.reportErr(errsurface.Error, errsurface.BuildSource, nodebuild.Notice(v, node))
		a.draw()
		return
	}
	keys := a.persist.out.Keys()
	ops := make([]string, len(keys))
	for i, k := range keys {
		ops[i] = k.Op
	}
	carryNotice(nodebuild.Unsaved(ops))
	a.reloadPage()
}

// reloadPage is errsurface.Reload. The URL is the place the reload lands on,
// so a pending write goes now. The reload takes the unload path every reload
// takes (flushOnUnload, closeAllURLStreams); the trace goes first, since it is
// how the reload is read back.
func (a *App) reloadPage() {
	a.writeURLNow()
	go func() {
		a.handOverBeforeReload()
		js.Global().Get("location").Call("reload")
	}()
}

// handOverBound caps how long a reload waits for its trace.
const handOverBound = 2 * time.Second

// handOverBeforeReload waits out a post already in flight, which carries only
// what was pending when it began, until the node holds every record or a post
// fails.
func (a *App) handOverBeforeReload() {
	deadline := time.Now().Add(handOverBound)
	for a.tr.PendingCount() > 0 && time.Now().Before(deadline) {
		if a.handOverTrace() != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// carriedNoticeKey is the one sessionStorage entry: the notice a page that
// reloads for its build hands the page that replaces it, read once. It is the
// tab's, so it reaches the reload and no other tab, and the errsurface strip
// dies with the page that would have shown it.
const carriedNoticeKey = "gridwell.build-notice"

// carryNotice leaves msg for the reloaded page; "" leaves nothing. Storage a
// browser blocks throws, and then the notice is only in the trace.
func carryNotice(msg string) {
	if msg == "" {
		return
	}
	defer func() { recover() }()
	if ss := js.Global().Get("sessionStorage"); ss.Truthy() {
		ss.Call("setItem", carriedNoticeKey, msg)
	}
}

// showCarriedNotice puts on the strip what the page before the reload could
// not save, once.
func (a *App) showCarriedNotice() {
	defer func() { recover() }()
	ss := js.Global().Get("sessionStorage")
	if !ss.Truthy() {
		return
	}
	v := ss.Call("getItem", carriedNoticeKey)
	if v.Type() != js.TypeString {
		return
	}
	ss.Call("removeItem", carriedNoticeKey)
	a.reportErr(errsurface.Error, errsurface.BuildSource, v.String())
}

// pageReloaded reports whether a reload loaded this page, read from the
// navigation entry the browser keeps for it.
func pageReloaded() bool {
	perf := js.Global().Get("performance")
	if !perf.Truthy() || perf.Get("getEntriesByType").Type() != js.TypeFunction {
		return false
	}
	nav := perf.Call("getEntriesByType", "navigation")
	if nav.Length() == 0 {
		return false
	}
	return nav.Index(0).Get("type").String() == "reload"
}
