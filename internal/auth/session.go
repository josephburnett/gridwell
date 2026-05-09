package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// SessionTokenBytes is the raw entropy length for session tokens. The hex
// encoding doubles the on-the-wire size; 32 bytes → 64 hex chars.
const SessionTokenBytes = 32

// NewSessionToken returns a random hex-encoded session token. Errors are only
// possible if the system random source fails, which is fatal for the server.
func NewSessionToken() (string, error) {
	buf := make([]byte, SessionTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
