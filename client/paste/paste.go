// Package paste owns a right-drop that pastes, onto a shell or a raw text
// pane: whether it lands (Refusal for a shell, TextRefusal for a text, read
// by the ghost and the commit alike) and what it pastes (Text), each tile's
// reference, its place URL (pane.EncodeURL) under an origin, or into a shell
// a file's absolute path.
package paste

import (
	"cmp"
	"net"
	"strings"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
)

// Refusal is why a drop of rows onto shell does not paste, "" when it does:
// every row must be of the node the shell's tmux runs on, since only that node
// spells a path or a reference its shell reaches. It reads ids alone.
func Refusal(rows []*pb.Tile, shell *pb.Tile) string {
	for _, r := range rows {
		if !rpc.SameNode(r.GetId(), rpc.ContentID(shell)) {
			return cmp.Or(r.GetAltText(), r.GetId()) + " is not on the shell's node"
		}
	}
	return ""
}

// TextRefusal is why a drop onto a text whose body takes no edits
// (textedit.ReadOnly) does not paste, "" when it does: the row's own reason,
// or that it takes none.
func TextRefusal(row *pb.Tile, readOnly bool) string {
	if !readOnly {
		return ""
	}
	return cmp.Or(row.GetReadOnly(), cmp.Or(row.GetAltText(), row.GetId())+" takes no edits")
}

// Into is the kind of pane a paste lands in.
type Into int

const (
	// IntoShell is typed into a tmux by the node owning it, where a path is
	// what a command reads.
	IntoShell Into = iota
	// IntoText is inserted into a body, where a reference is what a click
	// opens, so a file is its reference too.
	IntoText
)

// Text is what a paste of rows into a pane of kind into inserts, the words
// space-separated with no newline: into a shell a file's absolute path or any
// other row's reference, quoted where a shell would split it; into a text
// every row's reference as it is.
func Text(rows []*pb.Tile, origin string, into Into) string {
	words := make([]string, len(rows))
	for i, r := range rows {
		word, file := rpc.FilePath(r.GetId())
		if !file || into == IntoText {
			word = Reference(origin, r)
		}
		if into == IntoShell {
			word = Quote(word)
		}
		words[i] = word
	}
	return strings.Join(words, " ")
}

// Reference is t's place URL under origin: a door opens its grid, a workspace
// tile is the window's place, and any other tile is the leaf on its grid.
func Reference(origin string, t *pb.Tile) string {
	var st pane.URLState
	switch {
	case rpc.IsWorkspaceKind(t.GetKind()):
		st.Workspace = t.GetId()
	case rpc.IsDoorKind(t.GetKind()) && t.GetChildGridId() != "":
		st.Anchor = t.GetChildGridId()
	default:
		st.Anchor, st.TileIDs = t.GetGridId(), []string{t.GetId()}
	}
	return strings.TrimSuffix(origin, "/") + pane.EncodeURL(st)
}

const plain = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_./:@%+=,-"

// Quote leaves a word a shell reads as one word alone and single-quotes any
// other.
func Quote(s string) string {
	if s != "" && strings.Trim(s, plain) == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// BindOrigin is the origin a shell on the node's own machine reaches its web
// API by, from the address the API listens on: an unspecified host is
// localhost. "" for an address it cannot read.
func BindOrigin(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return ""
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}
