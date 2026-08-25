package security

import (
	"strings"
	"testing"
)

// TestPasswordHashIsSaltedAndVerifiable verifies the credential primitive.
func TestPasswordHashIsSaltedAndVerifiable(t *testing.T) {
	const password = "matchmaker2026"
	first, err := HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatalf("hash password again: %v", err)
	}
	if first == second {
		t.Fatal("expected a fresh salt for every hash")
	}
	if strings.Contains(first, password) {
		t.Fatal("the encoded hash must not contain the password")
	}
	parts := strings.Split(first, "$")
	if len(parts) != 4 || parts[0] != algorithm {
		t.Fatalf("unexpected hash encoding: %q", first)
	}

	ok, err := VerifyPassword(first, password)
	if err != nil {
		t.Fatalf("verify password: %v", err)
	}
	if !ok {
		t.Fatal("expected the correct password to verify")
	}
	ok, err = VerifyPassword(first, password+"x")
	if err != nil {
		t.Fatalf("verify wrong password: %v", err)
	}
	if ok {
		t.Fatal("expected a wrong password to be rejected")
	}
	// A hash produced with a different salt must still verify the same password.
	if ok, err := VerifyPassword(second, password); err != nil || !ok {
		t.Fatalf("expected the second hash to verify as well: %v", err)
	}
}

// TestPasswordHashRejectsMalformedInput covers the failure paths.
func TestPasswordHashRejectsMalformedInput(t *testing.T) {
	if _, err := HashPassword("   "); err == nil {
		t.Fatal("expected a blank password to be refused")
	}
	malformed := []string{
		"",
		"pbkdf2-sha256$120000$salt",
		"scrypt$120000$c2FsdA$ZGVyaXZlZA",
		"pbkdf2-sha256$abc$c2FsdA$ZGVyaXZlZA",
		"pbkdf2-sha256$0$c2FsdA$ZGVyaXZlZA",
		"pbkdf2-sha256$120000$!!!$ZGVyaXZlZA",
		"pbkdf2-sha256$120000$c2FsdA$",
	}
	for _, encoded := range malformed {
		if _, err := VerifyPassword(encoded, "whatever"); err == nil {
			t.Fatalf("expected %q to be reported as malformed", encoded)
		}
	}
}

// TestPBKDF2MatchesKnownVector checks the derivation against RFC 6070 style input
// so that a refactor cannot silently change stored credentials.
func TestPBKDF2MatchesKnownVector(t *testing.T) {
	derived := pbkdf2SHA256([]byte("password"), []byte("salt"), 1, 32)
	expected := []byte{
		0x12, 0x0f, 0xb6, 0xcf, 0xfc, 0xf8, 0xb3, 0x2c,
		0x43, 0xe7, 0x22, 0x52, 0x56, 0xc4, 0xf8, 0x37,
		0xa8, 0x65, 0x48, 0xc9, 0x2c, 0xcc, 0x35, 0x48,
		0x08, 0x05, 0x98, 0x7c, 0xb7, 0x0b, 0xe1, 0x7b,
	}
	if len(derived) != len(expected) {
		t.Fatalf("expected %d bytes, got %d", len(expected), len(derived))
	}
	for index := range expected {
		if derived[index] != expected[index] {
			t.Fatalf("derived key differs at byte %d: %#x vs %#x",
				index, derived[index], expected[index])
		}
	}
	// A longer key spans several HMAC blocks.
	long := pbkdf2SHA256([]byte("password"), []byte("salt"), 2, 40)
	if len(long) != 40 {
		t.Fatalf("expected 40 bytes, got %d", len(long))
	}
}

// TestSessionTokensAreOpaqueAndHashed verifies that only digests are storable.
func TestSessionTokensAreOpaqueAndHashed(t *testing.T) {
	plaintext, digest, err := NewSessionToken()
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if plaintext == "" || digest == "" {
		t.Fatal("expected both the token and its digest")
	}
	if plaintext == digest {
		t.Fatal("expected the stored digest to differ from the bearer token")
	}
	if len(digest) != 64 {
		t.Fatalf("expected a 64 character digest, got %d", len(digest))
	}
	if HashToken(plaintext) != digest {
		t.Fatal("expected the digest to be reproducible from the token")
	}
	other, _, err := NewSessionToken()
	if err != nil {
		t.Fatalf("issue second token: %v", err)
	}
	if other == plaintext {
		t.Fatal("expected two tokens to differ")
	}
	if fingerprint := Fingerprint(plaintext); len(fingerprint) != 12 || !strings.HasPrefix(digest, fingerprint) {
		t.Fatalf("unexpected fingerprint %q", fingerprint)
	}
	if Fingerprint("") != "" {
		t.Fatal("expected an empty token to have no fingerprint")
	}
}

// TestBearerTokenParsing covers the header contract.
func TestBearerTokenParsing(t *testing.T) {
	cases := map[string]string{
		"Bearer abc123":    "abc123",
		"bearer abc123":    "abc123",
		"BEARER  abc123  ": "abc123",
		"Basic abc123":     "",
		"abc123":           "",
		"Bearer":           "",
		"":                 "",
	}
	for header, expected := range cases {
		if got := BearerToken(header); got != expected {
			t.Fatalf("header %q: expected %q, got %q", header, expected, got)
		}
	}
}

// TestIdentifiersArePrefixedAndUnique verifies the identifier generator.
func TestIdentifiersArePrefixedAndUnique(t *testing.T) {
	seen := make(map[string]struct{}, 64)
	for index := 0; index < 64; index++ {
		id, err := NewID("mch")
		if err != nil {
			t.Fatalf("new id: %v", err)
		}
		if !strings.HasPrefix(id, "mch_") {
			t.Fatalf("expected a prefixed identifier, got %q", id)
		}
		if len(id) != len("mch_")+24 {
			t.Fatalf("unexpected identifier length for %q", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("identifier %q was generated twice", id)
		}
		seen[id] = struct{}{}
	}
	if id := MustNewID("usr"); !strings.HasPrefix(id, "usr_") {
		t.Fatalf("unexpected identifier %q", id)
	}
}
