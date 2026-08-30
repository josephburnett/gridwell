// Package doctype owns the filename-to-document-type classifications that
// both sides of the plugin seam read: the fs plugin decides from them what
// a file's descent body is, and the client decides how it renders. It is a
// neutral home importable from anywhere, so a server-side plugin never
// imports a client rendering package. One rule per fact: client/markdown
// re-exports these, so its render pipeline and this classification cannot
// disagree.
package doctype

import "strings"

// IsOrg reports whether name marks an org-mode document.
func IsOrg(name string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), ".org")
}

// Renderable reports whether a name marks content the document renderer
// handles. It is the one renderability rule: the fs plugin serves a
// renderable file's real bytes as the descent body, and the client colors
// file tiles by the same verdict, so what looks renderable and what
// actually renders cannot disagree.
func Renderable(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.HasSuffix(n, ".md") || strings.HasSuffix(n, ".markdown") || IsOrg(n)
}
