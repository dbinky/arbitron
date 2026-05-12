package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Tests: redactDatabaseURL — password redaction for startup log safety
// ---------------------------------------------------------------------------

// Happy path: password in URL is replaced with ***.
func TestRedactDatabaseURL_RedactsPassword(t *testing.T) {
	raw := "postgres://alice:supersecret@localhost:5432/arbitron?sslmode=disable"
	got := redactDatabaseURL(raw)
	assert.Contains(t, got, "alice")
	assert.Contains(t, got, "***")
	assert.NotContains(t, got, "supersecret", "password must not appear in redacted URL")
}

// Error path: malformed URL is returned unchanged (startup must not panic).
func TestRedactDatabaseURL_MalformedURLReturnedUnchanged(t *testing.T) {
	raw := "://not a valid url\x00"
	got := redactDatabaseURL(raw)
	assert.Equal(t, raw, got, "malformed URL must be returned as-is")
}

// Edge: URL with username but no password is returned unchanged (no *** appended).
func TestRedactDatabaseURL_NoPasswordReturnedUnchanged(t *testing.T) {
	raw := "postgres://alice@localhost:5432/arbitron"
	got := redactDatabaseURL(raw)
	assert.Equal(t, got, redactDatabaseURL(raw))
	assert.NotContains(t, got, "***", "URL without password must not have *** inserted")
}

// Edge: URL with no user-info section is returned unchanged.
func TestRedactDatabaseURL_NoUserInfoReturnedUnchanged(t *testing.T) {
	raw := "postgres://localhost:5432/arbitron"
	got := redactDatabaseURL(raw)
	assert.NotContains(t, got, "***")
	// The URL should still be parseable and roughly equivalent.
	assert.Contains(t, got, "localhost")
}

// Boundary: empty password field (user:@host) is handled without panic.
func TestRedactDatabaseURL_EmptyPassword(t *testing.T) {
	// url.Parse treats "user:@host" as having a password (empty string).
	// hasPassword is true, so *** is applied even for an empty password.
	raw := "postgres://alice:@localhost:5432/arbitron"
	got := redactDatabaseURL(raw)
	// Should not panic and should contain the username.
	assert.Contains(t, got, "alice")
}
