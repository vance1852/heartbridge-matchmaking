// Package security holds the credential primitives of the service: password
// hashing, opaque session token generation and identifier generation. It has no
// dependency on the domain so that it can be reused by any entry point.
package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// defaultIterations is the PBKDF2 work factor. It is stored inside every hash so
// that the factor can be raised later without invalidating existing users.
const defaultIterations = 120000

const (
	saltLength = 16
	keyLength  = 32
	algorithm  = "pbkdf2-sha256"
)

// ErrMalformedHash is returned when a stored credential cannot be parsed.
var ErrMalformedHash = errors.New("security: malformed password hash")

// HashPassword derives a PBKDF2-HMAC-SHA256 hash with a fresh random salt and
// encodes algorithm, work factor, salt and derived key into a single string.
func HashPassword(password string) (string, error) {
	if strings.TrimSpace(password) == "" {
		return "", errors.New("security: password must not be empty")
	}
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("security: read salt: %w", err)
	}
	derived := pbkdf2SHA256([]byte(password), salt, defaultIterations, keyLength)
	return strings.Join([]string{
		algorithm,
		strconv.Itoa(defaultIterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(derived),
	}, "$"), nil
}

// VerifyPassword recomputes the derived key with the parameters stored in the
// encoded hash and compares it in constant time.
func VerifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != algorithm {
		return false, ErrMalformedHash
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false, ErrMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false, ErrMalformedHash
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(expected) == 0 {
		return false, ErrMalformedHash
	}
	actual := pbkdf2SHA256([]byte(password), salt, iterations, len(expected))
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

// pbkdf2SHA256 implements PBKDF2 (RFC 8018) over HMAC-SHA256 using only the
// standard library so that the service keeps a minimal dependency surface.
func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	mac := hmac.New(sha256.New, password)
	hashLen := mac.Size()
	blocks := (keyLen + hashLen - 1) / hashLen
	output := make([]byte, 0, blocks*hashLen)
	block := make([]byte, 4)
	for index := 1; index <= blocks; index++ {
		mac.Reset()
		mac.Write(salt)
		block[0] = byte(index >> 24)
		block[1] = byte(index >> 16)
		block[2] = byte(index >> 8)
		block[3] = byte(index)
		mac.Write(block)
		current := mac.Sum(nil)
		accumulated := make([]byte, len(current))
		copy(accumulated, current)
		for iteration := 2; iteration <= iterations; iteration++ {
			mac.Reset()
			mac.Write(current)
			current = mac.Sum(current[:0])
			for i := range accumulated {
				accumulated[i] ^= current[i]
			}
		}
		output = append(output, accumulated...)
	}
	return output[:keyLen]
}
