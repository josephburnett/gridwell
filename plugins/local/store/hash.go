package store

import (
	"crypto/sha256"
	"encoding/hex"
)

// hashBytes returns the hex-encoded sha256 of data. Defined as a top-level
// helper so it stays out of the way and can be reused by both file creation
// and update.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
