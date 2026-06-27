package store

import (
	"crypto/rand"
	"encoding/hex"
)

// newUUID returns a random 128-bit identifier as a 32-character hex string.
//
// We use raw hex rather than the standard 8-4-4-4-12 grouping because no caller
// parses these — they exist only to compare object_id equality across rows.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// NewUUID returns a fresh random 128-bit id as a 32-character hex string. It is
// the exported entry point for callers outside the store (e.g. `gridwell init`
// minting a plugin id) so the id format stays defined in exactly one place.
func NewUUID() string { return newUUID() }
