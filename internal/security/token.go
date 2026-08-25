package security

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// tokenBytes is the entropy of an opaque session token.
const tokenBytes = 32

// NewSessionToken returns a fresh opaque bearer token together with the digest
// that is the only representation ever persisted. The plaintext is returned to
// the caller exactly once, at login time.
func NewSessionToken() (plaintext string, digest string, err error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("security: read token: %w", err)
	}
	plaintext = base64.RawURLEncoding.EncodeToString(raw)
	return plaintext, HashToken(plaintext), nil
}

// HashToken returns the lowercase hex SHA-256 digest of a bearer token. Storing
// only the digest keeps a database dump from being replayable as live sessions.
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// Fingerprint returns a short, non-reversible prefix of the token digest. It is
// the only token-derived value that may appear in logs.
func Fingerprint(plaintext string) string {
	if plaintext == "" {
		return ""
	}
	return HashToken(plaintext)[:12]
}

// NewID returns a sortable-prefixed random identifier such as "mch_9f2c...".
// Identifiers are opaque to clients and never encode business meaning.
func NewID(prefix string) (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("security: read id: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(raw), nil
}

// MustNewID is NewID for wiring code that cannot meaningfully recover from a
// failing system entropy source.
func MustNewID(prefix string) string {
	id, err := NewID(prefix)
	if err != nil {
		panic(err)
	}
	return id
}

// BearerToken extracts the token from an Authorization header value. It returns
// an empty string when the header is absent or not a Bearer credential.
func BearerToken(header string) string {
	const scheme = "bearer "
	if len(header) <= len(scheme) {
		return ""
	}
	if !strings.EqualFold(header[:len(scheme)], scheme) {
		return ""
	}
	return strings.TrimSpace(header[len(scheme):])
}
