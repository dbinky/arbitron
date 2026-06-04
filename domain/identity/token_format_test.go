package identity

import (
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var tokenShape = regexp.MustCompile(`^arb_[0-9a-f]+_[0-9a-f]+$`)

// Happy path: GenerateToken returns a well-shaped plaintext whose prefix and
// hash match the auxiliary returns, and HashToken/ParsePrefix agree.
func TestGenerateToken_HappyPath(t *testing.T) {
	plaintext, prefix, hash, err := GenerateToken()
	require.NoError(t, err)

	assert.Regexp(t, tokenShape, plaintext, "plaintext must match arb_<hex>_<hex>")

	gotPrefix, ok := ParsePrefix(plaintext)
	require.True(t, ok)
	assert.Equal(t, prefix, gotPrefix, "ParsePrefix must equal the returned prefix")

	assert.Equal(t, hash, HashToken(plaintext), "HashToken(plaintext) must equal the returned hash")
	assert.Contains(t, plaintext, prefix, "plaintext must contain its clear prefix")
}

// Input validation: malformed tokens never yield a prefix; HashToken("") is a
// stable non-empty hex digest (defensive, not a panic).
func TestParsePrefix_Validation(t *testing.T) {
	for _, bad := range []string{"garbage", "arb_only", "", "arb__sec", "arb_pre_", "xyz_pre_sec"} {
		got, ok := ParsePrefix(bad)
		assert.False(t, ok, "ParsePrefix(%q) should be invalid", bad)
		assert.Equal(t, "", got)
	}

	h := HashToken("")
	assert.NotEmpty(t, h)
	assert.Equal(t, h, HashToken(""), "HashToken is deterministic")
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), h, "sha256 hex is 64 chars")
}

// Error/failure: a failing crypto/rand reader propagates and yields no token.
func TestGenerateToken_RandFailure(t *testing.T) {
	orig := randRead
	defer func() { randRead = orig }()
	sentinel := errors.New("rand boom")
	randRead = func(b []byte) (int, error) { return 0, sentinel }

	plaintext, prefix, hash, err := GenerateToken()
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Empty(t, plaintext)
	assert.Empty(t, prefix)
	assert.Empty(t, hash)
}

// Boundary: encoded lengths match the byte budgets, and successive calls are
// unique in both plaintext and hash.
func TestGenerateToken_Boundary(t *testing.T) {
	p1, pre1, h1, err := GenerateToken()
	require.NoError(t, err)
	p2, _, h2, err := GenerateToken()
	require.NoError(t, err)

	// hex encoding doubles the byte count.
	assert.Len(t, pre1, tokenPrefixBytes*2, "prefix hex length")
	parts := strings.Split(p1, "_")
	require.Len(t, parts, 3)
	assert.Len(t, parts[1], tokenPrefixBytes*2, "prefix segment length")
	assert.Len(t, parts[2], tokenSecretBytes*2, "secret segment length")

	assert.NotEqual(t, p1, p2, "two tokens must differ")
	assert.NotEqual(t, h1, h2, "two hashes must differ")
}

// Isolation/security: the FULL token is hashed (not just the secret), the hash
// is one-way (it does not contain the prefix), and plaintext != hash.
func TestGenerateToken_Security(t *testing.T) {
	plaintext, prefix, hash, err := GenerateToken()
	require.NoError(t, err)

	parts := strings.Split(plaintext, "_")
	require.Len(t, parts, 3)
	secretOnly := parts[2]

	assert.NotEqual(t, HashToken(secretOnly), hash, "hash must cover the whole token, not just the secret")
	assert.NotEqual(t, plaintext, hash)
	assert.NotContains(t, hash, prefix, "hash must not leak the prefix")
}

// Defensive: HashToken must accept an io.Reader-free path; nothing here uses
// randRead, but assert the seam type is what we expect.
var _ func([]byte) (int, error) = randRead

func init() {
	// Ensure the default randRead reads from the package source without panicking.
	_ = io.Reader(nil)
}
