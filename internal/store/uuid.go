package store

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
)

// newUUID returns a random 128-bit identifier as a 32-character hex string.
//
// We use raw hex rather than the standard 8-4-4-4-12 grouping because no caller
// parses these — they exist only to compare object_id equality across rows.
// This is the PROVENANCE generator (object_id): it must stay 128-bit, so a
// cross-node "same origin" comparison never collides. Plugin/node identity
// uses the human-scale NewShortID instead (owner decision 2026-07-25).
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// NewUUID returns a fresh random 128-bit id as a 32-character hex string — the
// exported provenance (object_id) format, defined in exactly one place.
func NewUUID() string { return newUUID() }

// shortIDLen is the length of a plugin/node id minted by NewShortID. Seven
// characters of lowercase base36 with a leading letter is ~35.7 bits —
// birthday collision odds reach 50% around 300k ids, far beyond a personal
// federation — while staying readable in a URL path.
const shortIDLen = 7

// NewShortID returns a fresh plugin/node identity: shortIDLen characters of
// lowercase base36 whose FIRST character is a letter (owner decision
// 2026-07-25). The shape is load-bearing, not cosmetic:
//   - lowercase-only because the id names a directory (~/.gridwell/db/<id>)
//     on case-insensitive filesystems and a tmux socket;
//   - no '/' so the qualified-id codec (rpc.SplitID) stays delimiter-clean;
//   - the leading letter guarantees the id can never be purely numeric, which
//     is how URL paths and markdown embed hrefs tell a namespace segment from
//     a tile id (config.Load enforces the same rule on hand-edited ids).
//
// Existing plugins keep their 32-hex ids forever — an id is immutable once
// minted (it lives in other plugins' stored references, Electron session
// partitions, and tmux socket names); every consumer accepts both shapes.
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

// randBelow returns a uniform random int in [0, n) from crypto/rand — no
// modulo bias, so the id space keeps its full entropy.
func randBelow(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		// crypto/rand failing means the platform's entropy source is broken;
		// there is no meaningful degraded mode for identity minting.
		panic(err)
	}
	return int(v.Int64())
}
