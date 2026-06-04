package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	tokenPrefixBytes = 6  // ~12 hex chars of clear, non-secret prefix
	tokenSecretBytes = 32 // 256 bits of entropy in the secret half
	// TokenScheme is the fixed leading segment of every Arbitron token.
	TokenScheme = "arb"
)

// randRead is the crypto/rand seam. It is a package var so tests can inject a
// failing reader to prove error propagation. Production code MUST never reassign
// it; it always reads from crypto/rand.
var randRead = func(b []byte) (int, error) { return rand.Read(b) }

// GenerateToken returns a fresh plaintext token "arb_<prefix>_<secret>", its
// clear prefix, and the sha256-hex of the FULL plaintext. The plaintext is the
// only secret-bearing return; callers show it once and never persist it. Both
// halves come from crypto/rand and are lowercase-hex encoded so the two '_'
// separators stay unambiguous.
func GenerateToken() (plaintext, prefix, hash string, err error) {
	prefixRaw := make([]byte, tokenPrefixBytes)
	if _, err := randRead(prefixRaw); err != nil {
		return "", "", "", fmt.Errorf("generating token prefix: %w", err)
	}
	secretRaw := make([]byte, tokenSecretBytes)
	if _, err := randRead(secretRaw); err != nil {
		return "", "", "", fmt.Errorf("generating token secret: %w", err)
	}

	prefix = hex.EncodeToString(prefixRaw)
	secret := hex.EncodeToString(secretRaw)
	plaintext = TokenScheme + "_" + prefix + "_" + secret
	hash = HashToken(plaintext)
	return plaintext, prefix, hash, nil
}

// HashToken returns the sha256-hex of the ENTIRE presented token string. The
// middleware computes this from the raw Authorization header and looks the token
// up by hash, so the stored hash must cover the whole "arb_<prefix>_<secret>".
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// ParsePrefix extracts <prefix> from a well-formed arb_<prefix>_<secret>, or
// ("", false) if the token is not in the expected shape. It is a cheap
// pre-filter the middleware can run before hashing an arbitrary header value.
func ParsePrefix(plaintext string) (string, bool) {
	parts := strings.Split(plaintext, "_")
	if len(parts) != 3 {
		return "", false
	}
	if parts[0] != TokenScheme || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	return parts[1], true
}
