package todos

import (
	"fmt"
	"strings"
)

// The tile content is markdown: a text tile renders it on its grid face and,
// descended, as the read-only selectable document. A link in a rendered
// document opens an ephemeral visit in a split below, which is the whole point
// of the target link. A plugin-served HTML page cannot do that: it is
// sandboxed, and its links stay inside the sandbox.

// SnippetRunes bounds the body excerpt on the face.
const SnippetRunes = 240

// Snippet is the body's first SnippetRunes runes on one line.
func (t *Todo) Snippet() string {
	s := strings.Join(strings.Fields(t.Body), " ")
	if r := []rune(s); len(r) > SnippetRunes {
		return string(r[:SnippetRunes]) + "…"
	}
	return s
}

// From names who the todo is from: the author's display name, with the
// username when it differs.
func (t *Todo) From() string {
	name := strings.TrimSpace(t.Author.Name)
	user := strings.TrimSpace(t.Author.Username)
	switch {
	case name != "" && user != "" && !strings.EqualFold(name, user):
		return name + " (@" + user + ")"
	case name != "":
		return name
	case user != "":
		return "@" + user
	}
	return ""
}

// Markdown renders the todo as its tile content: the subject as the heading,
// who it is from and what they did, the project, the date, the body excerpt,
// and the link to the target — the issue or the merge request — rather than to
// the todo itself.
func Markdown(t *Todo) []byte {
	var b strings.Builder
	head := t.Title()
	if r := t.Ref(); r != "" {
		head = r + " " + head
	}
	if t.Done() {
		head = "✓ " + head
	}
	fmt.Fprintf(&b, "# %s\n\n", head)
	line := t.Action()
	if from := t.From(); from != "" {
		line += " — from " + from
	}
	if p := t.Project.PathWithNamespace; p != "" {
		line += " · " + p
	}
	line += " · " + t.CreatedAt.UTC().Format("2006-01-02")
	if t.Done() {
		line += " · done"
	}
	fmt.Fprintf(&b, "%s\n\n", line)
	if s := t.Snippet(); s != "" {
		fmt.Fprintf(&b, "> %s\n\n", s)
	}
	if t.TargetURL != "" {
		label := t.Ref()
		if label == "" {
			label = t.TargetType
		}
		fmt.Fprintf(&b, "[Open %s in GitLab](%s)\n", label, t.TargetURL)
	}
	return []byte(b.String())
}

// GoneMarkdown is the content for a key the memory does not hold. The node
// remembers the tile, because a todo never disappears, but this process has
// not seen its content since it started, so say so.
func GoneMarkdown(key string) []byte {
	return []byte("_This todo (`" + key + "`) is no longer in GitLab, or has not been seen since the plugin started._\n")
}
