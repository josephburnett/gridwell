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
