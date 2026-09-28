// Package urlview holds the decisions a live url view makes over the Electron
// bridge's facts. They live here rather than in client/wasm because `make
// check` executes this package and only compiles that one.
package urlview

// Capture is what a live view reports about its page: the last frame, the
// address it ended on, its title and its navigation trail.
type Capture struct {
	JPEG    []byte
	URL     string
	Title   string
	History string
}

// Writeback is what a closing view writes to its row, false for nothing. It
// is the one place a url tile's page and an internet url tile differ: a served
// page's address, title and history are its plugin's, so a page writes its
// frame alone. Only a freeze the caller asked for writes, and never an empty
// capture, which would overwrite a good face with nothing.
func Writeback(freeze, page bool, c Capture) (Capture, bool) {
	if page {
		c = Capture{JPEG: c.JPEG}
	}
	return c, freeze && (len(c.JPEG) > 0 || c.URL != "" || c.Title != "")
}

// DecideUnloadURLState decides what a dying page writes about one live url
// view; textedit.DecideUnloadFlush is the text arm of the same unload. The
// bridge's frame and trail are unreachable by then, so the address and title
// ride alone, through Writeback. An ephemeral visit belongs to no row and a
// page that never navigated has nothing new to write. The bridge's last
// reported address is the claim; the cached row's stands in when the bridge
// reported none.
func DecideUnloadURLState(page, durable, navDirty bool, lastURL, lastTitle, cachedURL string) (Capture, bool) {
	if !durable || !navDirty {
		return Capture{}, false
	}
	if lastURL == "" {
		lastURL = cachedURL
	}
	if lastURL == "" {
		return Capture{}, false
	}
	return Writeback(true, page, Capture{URL: lastURL, Title: lastTitle})
}

// Durable is whether a live view's descended row survives ascent, which gates
// the standing freeze, the writeback and the context menu's Freeze Page. Not
// known yet counts as ephemeral, because durable is a promise to write.
func Durable(possiblyEphemeral bool) bool {
	return !possiblyEphemeral
}
