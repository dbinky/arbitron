package provider_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
)

// Happy path: a public https endpoint passes StrictURLPolicy.
func TestStrictURLPolicy_PublicHTTPS_OK(t *testing.T) {
	for _, raw := range []string{
		"https://api.openrouter.ai",
		"https://example.openai.azure.com",
		"https://llama-serverless.eastus.models.ai.azure.com/chat/completions",
		"https://router.test:8443/v1",
	} {
		assert.NoError(t, provider.StrictURLPolicy.ValidateURL(raw), "url %q", raw)
	}
}

// Failure: a non-https scheme is rejected (http, file, gopher, and friends are
// the classic SSRF / credential-over-plaintext vectors).
func TestStrictURLPolicy_NonHTTPSScheme_Rejected(t *testing.T) {
	for _, raw := range []string{
		"http://api.openrouter.ai",
		"file:///etc/passwd",
		"gopher://evil.test",
		"ftp://host.test/x",
	} {
		err := provider.StrictURLPolicy.ValidateURL(raw)
		assert.ErrorIs(t, err, provider.ErrInvalidBaseURL, "url %q", raw)
	}
}

// Failure: obviously-internal hosts are rejected even over https — loopback,
// the cloud metadata endpoint, link-local, and RFC1918 private ranges.
func TestStrictURLPolicy_InternalHosts_Rejected(t *testing.T) {
	for _, raw := range []string{
		"https://localhost/v1",
		"https://127.0.0.1/v1",
		"https://127.0.0.8/v1",
		"https://[::1]/v1",
		"https://169.254.169.254/latest/meta-data/",
		"https://169.254.0.10/v1",
		"https://10.0.0.5/v1",
		"https://192.168.1.10/v1",
		"https://172.16.0.1/v1",
		"https://0.0.0.0/v1",
	} {
		err := provider.StrictURLPolicy.ValidateURL(raw)
		assert.ErrorIs(t, err, provider.ErrInvalidBaseURL, "url %q must be rejected", raw)
	}
}

// Error: an empty or unparseable base_url is rejected.
func TestStrictURLPolicy_EmptyOrUnparseable_Rejected(t *testing.T) {
	for _, raw := range []string{"", "   ", "https://%zz"} {
		err := provider.StrictURLPolicy.ValidateURL(raw)
		assert.ErrorIs(t, err, provider.ErrInvalidBaseURL, "url %q", raw)
	}
}

// Edge: a non-IP, non-localhost hostname that is not publicly resolvable is
// still accepted (the policy guards literal-internal addresses, not arbitrary
// hostnames — DNS resolution is deliberately not performed).
func TestStrictURLPolicy_UnknownHostname_OK(t *testing.T) {
	assert.NoError(t, provider.StrictURLPolicy.ValidateURL("https://x"))
}

// Success: AllowAnyURLPolicy accepts http and loopback for tests/fixtures.
func TestAllowAnyURLPolicy_AcceptsHTTPAndLoopback(t *testing.T) {
	for _, raw := range []string{
		"http://litellm.internal:4000",
		"http://127.0.0.1:8080",
		"https://example.com",
	} {
		assert.NoError(t, provider.AllowAnyURLPolicy.ValidateURL(raw), "url %q", raw)
	}
}

// Failure: AllowAnyURLPolicy still rejects an empty base_url.
func TestAllowAnyURLPolicy_RejectsEmpty(t *testing.T) {
	assert.ErrorIs(t, provider.AllowAnyURLPolicy.ValidateURL(""), provider.ErrInvalidBaseURL)
}

// Happy path: ValidateConnectionURL applies the policy and names the connection
// in the wrapped error.
func TestValidateConnectionURL_StrictRejectsInternal(t *testing.T) {
	c := provider.Connection{Name: "internal", Provider: "openrouter", BaseURL: "https://127.0.0.1/v1"}
	err := provider.ValidateConnectionURL(c, provider.StrictURLPolicy)
	assert.ErrorIs(t, err, provider.ErrInvalidBaseURL)
	assert.Contains(t, err.Error(), "internal")
}

// Success: ValidateConnectionURL with a nil policy defaults to strict.
func TestValidateConnectionURL_NilPolicyDefaultsStrict(t *testing.T) {
	c := provider.Connection{Name: "n", Provider: "openrouter", BaseURL: "http://x"}
	assert.ErrorIs(t, provider.ValidateConnectionURL(c, nil), provider.ErrInvalidBaseURL)
}
