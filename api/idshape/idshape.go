// Package idshape owns the Gridwell contract's identity shapes: the short id
// mint, the 128-bit mint behind system.plugin_uuid, and what a namespace
// segment must satisfy. It is in the api module so a plugin and the host
// agree without importing each other.
package idshape

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// NewUUID returns a random 128-bit id as 32 hex characters, ungrouped.
func NewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// shortIDLen gives about 35.7 bits: even odds of a collision near 300k ids.
const shortIDLen = 7

// NewShortID mints a plugin, node or namespace identity. Lowercase because
// the id names filesystem paths — a plugin's state_dir at ~/.gridwell/plugins/<id>,
// the home's tmux socket gridwell-<id>; no '/' so rpc.SplitID keeps its
// delimiter; a leading letter so a URL path tells a namespace segment from a
// tile id. The 32-hex shape stays valid forever.
func NewShortID() string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	const alnum = "0123456789abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, shortIDLen)
	b[0] = letters[randBelow(len(letters))]
	for i := 1; i < shortIDLen; i++ {
		b[i] = alnum[randBelow(len(alnum))]
	}
	return string(b)
}

// ValidateSegment rejects an id a URL path could not tell from a tile id.
// The tile shapes are decided by rpc.ShapeOf; api/rpc cannot be imported
// here because its tests import this package, so a test there pins the two
// spellings. what names the id in the error.
func ValidateSegment(what, id string) error {
	if id == "" {
		return fmt.Errorf("%s is empty — a namespace segment must name something", what)
	}
	if strings.Contains(id, "/") {
		return fmt.Errorf("%s %q must not contain '/'", what, id)
	}
	if _, err := strconv.ParseInt(id, 10, 64); err == nil {
		return fmt.Errorf("%s %q must not be purely numeric (indistinguishable from a tile id)", what, id)
	}
	if strings.HasPrefix(id, "~") {
		return fmt.Errorf("%s %q must not begin with '~' (indistinguishable from a key-form tile id)", what, id)
	}
	return nil
}

// randBelow draws from crypto/rand without modulo bias.
func randBelow(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic("idshape: crypto/rand failed: " + err.Error())
	}
	return int(v.Int64())
}
