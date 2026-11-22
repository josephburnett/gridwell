//go:build js && wasm

package main

import (
	"slices"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/shellconn"
	"github.com/josephburnett/gridwell/client/traceevent"
)

// localEchoTerm is a live terminal as shellconn.LocalEcho reads it: the
// guesses' row is an xterm marker, which follows the row through a scroll.
type localEchoTerm struct{ conn *shellStreamConn }

func (t localEchoTerm) Cols() int { return t.conn.term.Get("cols").Int() }

func (t localEchoTerm) Cursor() (int, bool) {
	buf := t.conn.term.Get("buffer").Get("active")
	line, ok := t.row()
	return buf.Get("cursorX").Int(), ok && line == buf.Get("baseY").Int()+buf.Get("cursorY").Int()
}

func (t localEchoTerm) Cell(col int) (string, bool) {
	n, ok := t.row()
	if !ok {
		return "", false
	}
	line := t.conn.term.Get("buffer").Get("active").Call("getLine", n)
	if !line.Truthy() {
		return "", false
	}
	if cell := line.Call("getCell", col); cell.Truthy() {
		return cell.Call("getChars").String(), true
	}
	return "", true
}

func (t localEchoTerm) row() (int, bool) {
	m := t.conn.echoRow
	if !m.Truthy() || m.Get("isDisposed").Bool() {
		return 0, false
	}
	return m.Get("line").Int(), true
}

// openLocalEcho gives a terminal its overlay, inside xterm's screen so it
// scrolls and hides with it, and never in its canvas, which a capture reads.
func (a *App) openLocalEcho(conn *shellStreamConn) {
	conn.echo = shellconn.NewLocalEcho()
	conn.echoNode = a.element("div", "", map[string]string{"position": "absolute", "display": "none",
		"pointerEvents": "none", "zIndex": "6", "whiteSpace": "pre"})
	conn.echoNode.Set("className", "gw-local-echo")
	if screen := conn.container.Call("querySelector", ".xterm-screen"); screen.Truthy() {
		screen.Call("appendChild", conn.echoNode)
	}
	conn.onParsed = js.FuncOf(func(js.Value, []js.Value) any {
		conn.echo.Output(nowMs(), localEchoTerm{conn})
		a.drawLocalEcho(conn)
		return nil
	})
	conn.term.Call("onWriteParsed", conn.onParsed)
}

func (a *App) localEchoKey(conn *shellStreamConn, data string) {
	if conn.echo.Pending() == 0 {
		if conn.echoRow.Truthy() {
			conn.echoRow.Call("dispose")
		}
		conn.echoRow = conn.term.Call("registerMarker", 0)
	}
	conn.echo.Key(data, nowMs(), localEchoTerm{conn})
	a.drawLocalEcho(conn)
}

// resetLocalEcho drops every guess: see shellconn.LocalEcho.Reset.
func (a *App) resetLocalEcho(conn *shellStreamConn) {
	if conn.echo != nil {
		conn.echo.Reset()
		a.drawLocalEcho(conn)
	}
}

// drawLocalEcho draws what the guesses say now, traces what they owe and
// arms their next deadline.
func (a *App) drawLocalEcho(conn *shellStreamConn) {
	now := nowMs()
	if flip, summary := conn.echo.Owed(); flip || summary {
		a.emit(traceevent.LocalEcho(conn.paneID, conn.tileID, conn.echo.Stats(), flip))
	}
	if due, ok := conn.echo.Next(now); ok && (conn.echoDue == 0 || due < conn.echoDue) {
		conn.echoDue = due
		setTimeoutMs(int(due-now)+1, func() {
			if conn.closed || conn.echoDue != due {
				return
			}
			conn.echoDue = 0
			conn.echo.Tick(nowMs())
			a.drawLocalEcho(conn)
		})
	}
	ov := conn.echo.Overlay(now)
	if slices.Equal(ov.Cells, conn.echoShown.Cells) && ov.Cursor == conn.echoShown.Cursor {
		return
	}
	conn.echoShown = ov
	conn.echoNode.Call("replaceChildren")
	if len(ov.Cells) == 0 {
		conn.echoNode.Get("style").Set("display", "none")
		return
	}
	a.e2e.localEchoDraws++
	first, last := min(ov.Cells[0].Col, ov.Cursor), ov.Cursor
	text := map[int]shellconn.LocalEchoCell{}
	for _, c := range ov.Cells {
		text[c.Col] = c
		first, last = min(first, c.Col), max(last, c.Col)
	}
	for col := first; col <= last; col++ {
		style := map[string]string{"display": "inline-block", "height": "100%"}
		c, ok := text[col]
		switch {
		case ok:
			style["background"] = a.pal.Bg
			if c.Underline {
				style["textDecoration"] = "underline"
			}
		case col == ov.Cursor:
			style["background"] = a.pal.ShellCursor
		}
		span := a.element("span", "", style)
		span.Set("textContent", c.Text)
		conn.echoNode.Call("appendChild", span)
	}
	conn.echoFirst = first
	a.placeLocalEcho(conn)
}

// placeLocalEcho puts the overlay on its cells at xterm's own cell size,
// which the screen element is cols by rows of.
func (a *App) placeLocalEcho(conn *shellStreamConn) {
	if len(conn.echoShown.Cells) == 0 {
		return
	}
	line, ok := localEchoTerm{conn}.row()
	screen := conn.echoNode.Get("parentNode")
	if !ok || !screen.Truthy() {
		conn.echoNode.Get("style").Set("display", "none")
		return
	}
	cw := screen.Get("clientWidth").Float() / conn.term.Get("cols").Float()
	ch := screen.Get("clientHeight").Float() / conn.term.Get("rows").Float()
	row := line - conn.term.Get("buffer").Get("active").Get("viewportY").Int()
	opts := conn.term.Get("options")
	style := conn.echoNode.Get("style")
	style.Set("display", "block")
	style.Set("color", a.pal.TextFg)
	style.Set("fontFamily", opts.Get("fontFamily"))
	style.Set("fontSize", pxf(opts.Get("fontSize").Float()))
	style.Set("lineHeight", pxf(ch))
	setBoundsPx(style, float64(conn.echoFirst)*cw, float64(row)*ch,
		cw*float64(conn.echoNode.Get("childElementCount").Int()), ch)
	for i, kids := 0, conn.echoNode.Get("children"); i < kids.Length(); i++ {
		kids.Index(i).Get("style").Set("width", pxf(cw))
	}
}

// closeLocalEcho traces the attachment's summary; the overlay goes with the
// terminal's element.
func (a *App) closeLocalEcho(conn *shellStreamConn) {
	if conn.echo != nil && conn.echo.Stats().Made > 0 {
		a.emit(traceevent.LocalEcho(conn.paneID, conn.tileID, conn.echo.Stats(), false))
	}
}
