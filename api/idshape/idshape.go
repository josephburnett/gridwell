// Package idshape owns the IDENTITY SHAPES of the Gridwell contract —
// facts every module shares and none may re-derive: the short plugin/node
// id mint, the 128-bit random mint behind system.plugin_uuid, and the
// validity rules a namespace segment must satisfy. Moved out of the localdb store
// (2026-08-15): id shape is CONTRACT, not storage — a third-party plugin
// minting a connection namespace and the host validating a hand-edited
// server.yaml must agree without either importing the other.
package idshape

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// NewUUID returns a fresh random 128-bit id as a 32-character hex string,
// defined in exactly one place. Raw hex rather than 8-4-4-4-12 grouping
// because no caller parses these. (It also minted the per-row object_id
// provenance marker until schema v10 retired that column — nothing read
// it to decide anything.)
func NewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// shortIDLen is the length of a plugin/node id minted by NewShortID. Seven
// characters of lowercase base36 with a leading letter is ~35.7 bits —
// birthday collision odds reach 50% around 300k ids, far beyond a personal
// federation — while staying readable in a URL path.
const shortIDLen = 7

// NewShortID returns a fresh plugin/node/namespace identity: shortIDLen
// characters of lowercase base36 whose FIRST character is a letter (owner
// decision 2026-07-25). The shape is load-bearing, not cosmetic:
//   - lowercase-only because the id names a directory (~/.gridwell/db/<id>)
//     on case-insensitive filesystems and a tmux socket;
//   - no '/' so the qualified-id codec (rpc.SplitID) stays delimiter-clean;
//   - the leading letter guarantees the id can never be purely numeric,
//     which is how URL paths tell a namespace segment from a tile id
//     (ValidateSegment enforces the same rules on hand-edited ids).
//
// Existing ids of the older 32-hex shape stay valid forever — an id is
// immutable once minted (it lives in other plugins' stored references,
// session partitions, and socket names); every consumer accepts both.
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

// ValidateSegment enforces the two load-bearing properties on an id used
// as a namespace segment (a plugin id, a node id, a connection namespace):
// no '/' (the qualified-id delimiter) and never purely numeric
// (indistinguishable from a tile id in a URL path). `what` names the id in
// the error.
func ValidateSegment(what, id string) error {
	if id == "" {
		return nil
	}
	if strings.Contains(id, "/") {
		return fmt.Errorf("%s %q must not contain '/'", what, id)
	}
	if _, err := strconv.ParseInt(id, 10, 64); err == nil {
		return fmt.Errorf("%s %q must not be purely numeric (indistinguishable from a tile id)", what, id)
	}
	return nil
}

// randBelow returns a uniform random int in [0, n) from crypto/rand — no
// modulo bias, so the id space keeps its full entropy.
func randBelow(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic("idshape: crypto/rand failed: " + err.Error())
	}
	return int(v.Int64())
}
