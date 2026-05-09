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
