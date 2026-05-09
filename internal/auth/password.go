// Package auth implements password hashing and session token primitives for
// Ascent.
//
// Passwords are hashed with argon2id and stored in the PHC string format so
// that hash parameters can be upgraded over time without breaking existing
// records (each stored hash carries the parameters it was generated with).
//
// The package has no dependency on the storage layer; the store calls in to
// hash on user creation and to verify on login. Session token generation is
// here too because both halves of authentication belong together.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2 parameters. These match the recommended OWASP defaults for argon2id
// at the time of writing. The hash format embeds them so future tuning does
// not invalidate already-stored hashes.
const (
	defaultTime    uint32 = 2
	defaultMemory  uint32 = 64 * 1024 // 64 MiB
	defaultThreads uint8  = 1
	defaultKeyLen  uint32 = 32
	defaultSaltLen uint32 = 16
)

// Params holds the cost parameters used to derive a hash. They are tunable in
// tests so the suite stays fast (production callers should pass nil to use
// the package defaults).
type Params struct {
	Time    uint32
	Memory  uint32
	Threads uint8
	KeyLen  uint32
	SaltLen uint32
}

// DefaultParams returns the production defaults.
func DefaultParams() *Params {
	return &Params{
		Time: defaultTime, Memory: defaultMemory, Threads: defaultThreads,
		KeyLen: defaultKeyLen, SaltLen: defaultSaltLen,
	}
}

// HashPassword returns an argon2id PHC-string hash of the password. Each call
// generates a fresh random salt; identical passwords therefore hash to
// distinct outputs.
func HashPassword(password string, p *Params) (string, error) {
	if p == nil {
		p = DefaultParams()
	}
	if password == "" {
		return "", errors.New("password is empty")
	}
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return formatHash(p, salt, key), nil
}

// VerifyPassword returns nil if the password matches the encoded hash. The
// comparison is constant-time. It returns an error if the hash format is
// invalid or the password is wrong (callers should not distinguish the two
// cases when reporting to the user).
func VerifyPassword(password, encoded string) error {
	p, salt, want, err := parseHash(encoded)
	if err != nil {
		return err
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	if subtle.ConstantTimeCompare(want, got) != 1 {
		return errors.New("password mismatch")
	}
	return nil
}

func formatHash(p *Params, salt, key []byte) string {
	enc := base64.RawStdEncoding
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads,
		enc.EncodeToString(salt), enc.EncodeToString(key),
	)
}

func parseHash(encoded string) (*Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// "$argon2id$v=...$m=...,t=...,p=...$salt$key" → 6 parts after split
	// because of the leading empty segment.
	if len(parts) != 6 {
		return nil, nil, nil, errors.New("invalid hash format")
	}
	if parts[1] != "argon2id" {
		return nil, nil, nil, errors.New("not argon2id")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return nil, nil, nil, fmt.Errorf("parse version: %w", err)
	}
	if version != argon2.Version {
		return nil, nil, nil, fmt.Errorf("unsupported version %d", version)
	}
	p := &Params{}
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &threads); err != nil {
		return nil, nil, nil, fmt.Errorf("parse params: %w", err)
	}
	p.Threads = threads
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("decode salt: %w", err)
	}
	key, err := enc.DecodeString(parts[5])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("decode key: %w", err)
	}
	p.SaltLen = uint32(len(salt))
	p.KeyLen = uint32(len(key))
	return p, salt, key, nil
}
