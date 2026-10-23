package procenv_test

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// BoundedHTTPClient always returns a client with a positive timeout, falling
// back to the default for a non-positive input so the unbounded net/http default
// client is never used on the OIDC/JWKS path.
func TestBoundedHTTPClient(t *testing.T) {
	t.Run("explicit timeout honored", func(t *testing.T) {
		c := procenv.BoundedHTTPClient(3 * time.Second)
		require.NotNil(t, c)
		assert.Equal(t, 3*time.Second, c.Timeout)
	})
	t.Run("non-positive falls back to default", func(t *testing.T) {
		for _, d := range []time.Duration{0, -1} {
			c := procenv.BoundedHTTPClient(d)
			require.NotNil(t, c)
			assert.Equal(t, procenv.DefaultOIDCDiscoveryTimeout, c.Timeout)
			assert.Positive(t, c.Timeout, "must never be the unbounded default client")
		}
	})
}

// BoundedReader returns the body unchanged for a non-positive cap.
func TestBoundedReader_NoCap(t *testing.T) {
	const body = "hello world"
	for _, max := range []int64{0, -1} {
		r := procenv.BoundedReader(strings.NewReader(body), max)
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, body, string(got))
	}
}

// A body within or exactly at the cap reads fully without error.
func TestBoundedReader_WithinCap(t *testing.T) {
	const body = "1234567890"
	for _, max := range []int64{int64(len(body)), int64(len(body)) + 1} {
		r := procenv.BoundedReader(strings.NewReader(body), max)
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, body, string(got))
	}
}

// A body exceeding the cap fails with ErrResponseTooLarge (hard failure, not EOF).
func TestBoundedReader_OverCapErrors(t *testing.T) {
	r := procenv.BoundedReader(strings.NewReader("1234567890"), 4)
	_, err := io.ReadAll(r)
	require.Error(t, err)
	assert.ErrorIs(t, err, procenv.ErrResponseTooLarge)
}

// ScrubControl replaces control chars with spaces and preserves printable +
// multibyte UTF-8.
func TestScrubControl(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"plain text", "plain text"},
		{"line1\nline2", "line1 line2"},
		{"a\tb\rc", "a b c"},
		{"esc\x1b[31m", "esc [31m"},
		{"null\x00byte", "null byte"},
		{"del\x7f", "del "},
		{"café—ok", "café—ok"}, // multibyte UTF-8 preserved
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, procenv.ScrubControl(tc.in), "in=%q", tc.in)
	}
}

// StatusSnippet caps at 512 bytes and scrubs control chars so a hostile upstream
// body cannot inject newlines into logs or flood them.
func TestStatusSnippet_CapsAndScrubs(t *testing.T) {
	// Newline is scrubbed to a space.
	assert.Equal(t, "a b", procenv.StatusSnippet(strings.NewReader("a\nb")))

	// Over 512 bytes is truncated to exactly 512.
	big := strings.Repeat("x", 600)
	got := procenv.StatusSnippet(strings.NewReader(big))
	assert.Len(t, got, 512)
	assert.Equal(t, strings.Repeat("x", 512), got)
}

// StatusSnippet returns the bytes read before a mid-stream read error
// (best-effort diagnostics), still scrubbed.
func TestStatusSnippet_ReadErrorBestEffort(t *testing.T) {
	r := io.MultiReader(strings.NewReader("part\nial"), errReader{})
	got := procenv.StatusSnippet(r)
	assert.Equal(t, "part ial", got)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }
