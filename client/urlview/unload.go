// Package urlview holds the decisions a live url view makes over the Electron
// bridge's facts. They live here rather than in client/wasm because `make
// check` executes this package and only compiles that one.
package urlview

// DecideUnloadURLState decides what a dying page writes about one live url
// view; textedit.DecideUnloadFlush is the text arm of the same unload. A
// serves_page view's address belongs to its plugin and an ephemeral visit to
// no row, so neither writes, and a page that never navigated has nothing the
// store does not hold. The bridge's last reported address is the claim; the
// cached row's stands in when the bridge reported none.
func DecideUnloadURLState(page, durable, navDirty bool, lastURL, cachedURL string) (url string, write bool) {
	if page || !durable || !navDirty {
		return "", false
	}
	if lastURL == "" {
		lastURL = cachedURL
	}
	return lastURL, lastURL != ""
}

// Durable is whether a live view's descended row survives ascent, which gates
// the standing freeze, the history writeback and the context menu's Freeze
// Page. A page view is not: its plugin owns the frozen face. An ephemeral
// visit is not, and not known yet counts as ephemeral, because durable is a
// promise to write.
func Durable(page, possiblyEphemeral bool) bool {
	return !page && !possiblyEphemeral
}

// PersistFreeze is whether a closing view's capture is written back: only a
// freeze the caller asked for, never a page view, and never an empty capture,
// which would overwrite a good face with nothing.
func PersistFreeze(freeze, page bool, jpeg []byte, url, title string) bool {
	return freeze && !page && (len(jpeg) > 0 || url != "" || title != "")
}
