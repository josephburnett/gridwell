package store

import (
	"crypto/sha256"
	"encoding/hex"
)

// hashBytes returns the hex-encoded sha256 of data.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
