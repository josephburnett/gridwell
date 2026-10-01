// Package urlview holds the decisions a live url view makes over the Electron
// bridge's facts. They live here rather than in client/wasm because `make
// check` executes this package and only compiles that one.
package urlview

import "github.com/josephburnett/gridwell/api/rpc"

// Capture is what a closing view reports about its page besides the address:
// the last frame, its title and its navigation trail. None of it is content.
type Capture struct {
	JPEG    []byte
	Title   string
	History string
}

// Owns reports that a url row's address, title and trail are the node's to
// write, not a plugin's. A grid not yet read is attempted: the server's
// verdict is the authority.
func Owns(page, gridWritable, gridKnown bool) bool {
	return !page && (gridWritable || !gridKnown)
}

// WriteAddress decides whether the address a live view landed on is written
// to its row as content, like a typed one. A visit that goes nowhere leaves
// the row byte-identical.
func WriteAddress(durable, owns bool, landed, stored string) bool {
	return durable && owns && rpc.HTTPAddress(landed) && landed != stored
}

// Writeback is what a closing view writes to its row as captures, false for
// nothing. A row the node does not own writes its frame alone, and an empty
// capture never overwrites a good face.
func Writeback(freeze, owns bool, c Capture) (Capture, bool) {
	if !owns {
		c = Capture{JPEG: c.JPEG}
	}
	return c, freeze && (len(c.JPEG) > 0 || c.Title != "" || c.History != "")
}

// DecideUnloadURLState decides what captures a dying page writes about one
// live view; the landed address is the content flush's. The bridge's frame and
// trail are unreachable by then, so the title rides alone.
func DecideUnloadURLState(owns, durable, navDirty bool, lastTitle string) (Capture, bool) {
	if !durable || !navDirty {
		return Capture{}, false
	}
	return Writeback(true, owns, Capture{Title: lastTitle})
}

// Durable is whether a live view's descended row survives ascent. Not known
// yet counts as ephemeral, because durable is a promise to write.
func Durable(possiblyEphemeral bool) bool {
	return !possiblyEphemeral
}
