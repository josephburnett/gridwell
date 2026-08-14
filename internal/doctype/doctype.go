// Package doctype owns the FILE-NAME → document-type classifications that
// both sides of the plugin seam read: the fs plugin decides what a file's
// descent body IS from them, and the wasm client decides how it renders.
// They lived in client/markdown, which made server-side plugins import a
// client rendering package (a layering violation the 2026-08-13 coupling
// audit flagged); this neutral home is importable from anywhere. One rule
// per fact — client/markdown re-exports these so its render pipeline and
// this classification can never disagree.
package doctype

import "strings"

// IsOrg reports whether name marks an org-mode document.
func IsOrg(name string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), ".org")
}

// Renderable reports whether a name marks content the document renderer
// handles (issue #236) — THE renderability rule: the fs plugin serves a
// renderable file's real bytes as the descent body, and the client colors
// file tiles by the same verdict (green vs grey), so what looks renderable
// and what actually renders can never disagree.
func Renderable(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.HasSuffix(n, ".md") || strings.HasSuffix(n, ".markdown") || IsOrg(n)
}
