//go:build js && wasm

package main

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"syscall/js"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/fileinfo"
	"github.com/josephburnett/gridwell/client/inflight"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/textedit"
)

// The file information view: one DOM overlay over the focused descent of a
// file in an info or hex mode (textedit.FileView), holding the file
// information table, the see more row and, in hex mode, the hex view, which
// loads fileinfo.PageBytes at a time through the content API as it is
// scrolled; and the "i" button that opens it on any other file. Every
// decision is fileinfo's or textedit's.

// fileView is the overlay and what it shows: the file whose content id and
// stamp it was read for, its FileInfo, and the bytes loaded so far, one text
// node per page.
type fileView struct {
	root, table, more, header, hex, toggle js.Value

	id, stamp string
	info      *gridwellv1.FileInfo
	asked     bool
	buf       []byte
	size      int64
	pages     []js.Value
	paging    bool
	failed    bool
	restored  bool
}

func (a *App) ensureFileView() *fileView {
	if v := a.overlays.fileView; v != nil {
		return v
	}
	v := &fileView{}
	el := func(tag, id string, style map[string]string) js.Value {
		e := a.doc.Call("createElement", tag)
		e.Set("id", id)
		for k, val := range style {
			e.Get("style").Set(k, val)
		}
		return e
	}
	v.root = el("div", "gw-file-info", map[string]string{"position": "absolute", "display": "none",
		"overflow": "auto", "boxSizing": "border-box", "padding": "8px", "zIndex": "5",
		"fontFamily": a.defaultMarkdownStyle().monospace, "fontSize": "13px", "lineHeight": "1.35"})
	v.table = el("table", "gw-file-info-table", map[string]string{"borderSpacing": "12px 2px"})
	v.more = el("div", "gw-file-see-more", map[string]string{"cursor": "pointer", "textDecoration": "underline", "margin": "8px 12px"})
	v.header = el("div", "gw-file-hex-header", map[string]string{"margin": "8px 12px 4px"})
	v.hex = el("pre", "gw-file-hex", map[string]string{"margin": "0 12px"})
	for _, c := range []js.Value{v.table, v.more, v.header, v.hex} {
		v.root.Call("appendChild", c)
	}
	v.root.Call("addEventListener", "scroll", js.FuncOf(func(js.Value, []js.Value) any {
		a.onFileViewScroll()
		return nil
	}))
	a.installOverlayMouse(v.root, func(ev js.Value, sx, sy float64) bool {
		if p := a.tree.FocusedPane(); p != nil && ev.Get("button").Int() == 0 &&
			pointInFileInner(paneRectFor(a, p), sx, sy) && v.more.Call("contains", ev.Get("target")).Bool() {
			a.onSeeMore()
		}
		return a.claimTextPress(ev, sx, sy)
	})
	a.installTextareaTouch(v.root)
	a.doc.Get("body").Call("appendChild", v.root)
	v.toggle = a.ensureInfoToggle()
	a.overlays.fileView = v
	return v
}

// refreshFileView shows, positions and fills the view for the focused pane,
// or hides it.
func (a *App) refreshFileView() {
	v := a.ensureFileView()
	a.refreshInfoToggle(v)
	p, t, r, d := a.focusedTextDescent()
	if t == nil || !textedit.FileView(d.Mode) || r.W <= 0 || r.H <= 0 {
		v.root.Get("style").Set("display", "none")
		return
	}
	row, _ := a.contentRow(t)
	if id := rpc.ContentID(t); id != v.id || row.GetContentStamp() != v.stamp {
		a.resetFileView(v, id, row.GetContentStamp())
	}
	s := v.root.Get("style")
	x, y, w, h := textInnerBox(r)
	setBoundsPx(s, x, y, w, h)
	s.Set("background", a.pal.FileInnerBg)
	s.Set("color", a.pal.TextFg)
	s.Set("clipPath", a.overlayClip(p))
	s.Set("display", "block")
	if !v.asked {
		v.asked = true
		go a.readFileInfo(v, v.id)
	}
	hex := d.Mode == rpc.TextModeHex
	more, display := "see more: its bytes in hex", "none"
	if hex {
		more, display = "see less", "block"
	}
	v.more.Set("textContent", more)
	v.header.Get("style").Set("display", display)
	v.hex.Get("style").Set("display", display)
	if hex && len(v.pages) == 0 {
		a.nextFilePage(v)
	}
	a.restoreFileViewScroll(p, v, hex)
}

func (a *App) resetFileView(v *fileView, id, stamp string) {
	v.id, v.stamp, v.info, v.asked = id, stamp, nil, false
	v.buf, v.size, v.pages, v.paging, v.failed, v.restored = nil, 0, nil, false, false, false
	v.table.Set("textContent", "reading…")
	v.header.Set("textContent", "")
	v.hex.Set("textContent", "")
	v.root.Set("scrollTop", 0)
}

// readFileInfo asks the node for id's file information and fills the table,
// unless the view has moved on to another file meanwhile.
func (a *App) readFileInfo(v *fileView, id string) {
	ctx, cancel := inflight.Bounded()
	defer cancel()
	info, err := a.cl.ReadInfo(ctx, id)
	if v.id != id {
		return
	}
	v.table.Set("textContent", "")
	if err != nil {
		a.surfaceRPCError("ReadContent", err)
		return
	}
	v.info = info
	for _, r := range fileinfo.Rows(info, time.Local) {
		tr := a.doc.Call("createElement", "tr")
		for i, text := range []string{r.Label, r.Value} {
			td := a.doc.Call("createElement", "td")
			td.Set("textContent", text)
			if i == 0 {
				td.Get("style").Set("opacity", "0.7")
			}
			tr.Call("appendChild", td)
		}
		v.table.Call("appendChild", tr)
	}
	a.refreshFileView()
}

// nextFilePage asks the content API for the page after the bytes loaded,
// the first page before the size is known.
func (a *App) nextFilePage(v *fileView) {
	rng, more := fileinfo.NextPage(int64(len(v.buf)), v.size)
	if len(v.pages) == 0 {
		rng, more = "bytes=0-"+strconv.Itoa(fileinfo.PageBytes-1), true
	}
	if !more || v.paging || v.failed {
		return
	}
	v.paging = true
	id := v.id
	go func() {
		data, size, err := a.fetchFileBytes(id, rng)
		if v.id != id {
			return
		}
		v.paging = false
		if err != nil {
			v.failed = true
			a.reportErr(errsurface.Error, "hexview", "the bytes of "+id+" did not load: "+err.Error())
			return
		}
		a.appendFilePage(v, data, size)
	}()
}

// fetchFileBytes is one byte range of id through the content API, and the
// file's size its answer names.
func (a *App) fetchFileBytes(id, rng string) ([]byte, int64, error) {
	ctx, cancel := inflight.Bounded()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rpc.PageURL(a.origin, a.contentToken, id), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Range", rng)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	switch {
	case err != nil:
		return nil, 0, err
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		return nil, 0, nil // an empty file
	case resp.StatusCode == http.StatusOK:
		return data, int64(len(data)), nil
	case resp.StatusCode != http.StatusPartialContent:
		return nil, 0, errors.New(resp.Status)
	}
	size, ok := fileinfo.RangeSize(resp.Header.Get("Content-Range"))
	if !ok {
		return nil, 0, errors.New("no size in " + resp.Header.Get("Content-Range"))
	}
	return data, size, nil
}

// appendFilePage lays the new page out after the last, laying the page
// before out again, since the new bytes may complete a
// rune its last row cut.
func (a *App) appendFilePage(v *fileView, data []byte, size int64) {
	before := len(v.buf)
	v.buf, v.size = append(v.buf, data...), size
	if n := len(v.pages); n > 0 {
		v.pages[n-1].Set("data", hexText(v.buf, (n-1)*fileinfo.PageBytes, before))
	}
	node := a.doc.Call("createTextNode", hexText(v.buf, before, len(v.buf)))
	v.hex.Call("appendChild", node)
	v.pages = append(v.pages, node)
	v.header.Set("textContent", fileinfo.HexHeader(int64(len(v.buf)), v.size))
	a.onFileViewScroll()
}

func hexText(buf []byte, from, to int) string {
	var b strings.Builder
	for _, l := range fileinfo.HexLines(buf, from, to) {
		b.WriteString(l.Offset + "  " + l.Hex + "  " + l.Text + "\n")
	}
	return b.String()
}

// onFileViewScroll keeps the view's scroll as the tile's text scroll and
// asks for the next page within a screen of the end.
func (a *App) onFileViewScroll() {
	v := a.overlays.fileView
	p, _, _, d := a.focusedTextDescent()
	if v == nil || p == nil || !textedit.FileView(d.Mode) {
		return
	}
	hex := d.Mode == rpc.TextModeHex
	el := v.root
	top := el.Get("scrollTop").Float()
	if v.restored {
		a.scrollText(p, 0, top/a.textScaleFor(p))
	}
	if hex && len(v.pages) > 0 && fileinfo.WantMore(top, el.Get("clientHeight").Float(), el.Get("scrollHeight").Float()) {
		a.nextFilePage(v)
	}
	a.restoreFileViewScroll(p, v, hex)
}

// restoreFileViewScroll opens the view where the tile was left once the
// bytes to scroll there are loaded, asking for pages until they are.
func (a *App) restoreFileViewScroll(p *pane.Pane, v *fileView, hex bool) {
	if v.restored || v.info == nil || hex && len(v.pages) == 0 {
		return
	}
	el := v.root
	want := p.TextScrollY * a.textScaleFor(p)
	if hex && want > el.Get("scrollHeight").Float()-el.Get("clientHeight").Float() && int64(len(v.buf)) < v.size {
		a.nextFilePage(v)
		return
	}
	el.Set("scrollTop", want)
	v.restored = true
}

// onSeeMore opens the hex view under the file information, or closes it.
func (a *App) onSeeMore() {
	p, _, _, d := a.focusedTextDescent()
	if p == nil || !textedit.FileView(d.Mode) {
		return
	}
	p.TextMode = rpc.TextModeHex
	if d.Mode == rpc.TextModeHex {
		p.TextMode = rpc.TextModeInfo
	}
	a.refreshFileOverlay()
	a.draw()
	a.scheduleURLUpdate()
}
