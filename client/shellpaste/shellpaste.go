// Package shellpaste owns a drop onto a shell: whether it lands (Refusal,
// read by the ghost and the commit alike) and the text the node owning the
// shell types into its tmux (Text): a file's absolute path, or any other
// tile's reference, its place URL (pane.EncodeURL) under an origin.
package shellpaste

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
	node := rpc.NamespaceOf(rpc.ContentID(shell))
	for _, r := range rows {
		if rpc.NamespaceOf(r.GetId()) != node {
			return cmp.Or(r.GetAltText(), r.GetId()) + " is not on the shell's node"
		}
	}
	return ""
}

// Text is what a paste of rows types: each row's path or reference, quoted
// where a shell would split it, space-separated, with no newline.
func Text(rows []*pb.Tile, origin string) string {
	words := make([]string, len(rows))
	for i, r := range rows {
		word, file := rpc.FilePath(r.GetId())
		if !file {
			word = Reference(origin, r)
		}
		words[i] = Quote(word)
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
