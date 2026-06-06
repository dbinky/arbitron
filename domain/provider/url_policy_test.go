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

// Failure: AllowAnyURLPolicy still rejects an empty or whitespace-only base_url.
func TestAllowAnyURLPolicy_RejectsEmpty(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t"} {
		assert.ErrorIs(t, provider.AllowAnyURLPolicy.ValidateURL(raw), provider.ErrInvalidBaseURL, "url %q", raw)
	}
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

// --- Provider-aware local-Ollama loopback carve-out (Phase 1, Task 2) ---

// Happy-path: local Ollama over http on a loopback host is allowed even though
// StrictURLPolicy would otherwise reject http + loopback.

func TestValidateConnectionURL_OllamaLocalhostHTTP_Allowed(t *testing.T) {
	c := provider.Connection{Name: "ollama-local", Provider: "ollama", BaseURL: "http://localhost:11434/v1"}
	assert.NoError(t, provider.ValidateConnectionURL(c, provider.StrictURLPolicy))
}

func TestValidateConnectionURL_Ollama127001HTTP_Allowed(t *testing.T) {
	c := provider.Connection{Name: "ollama-local", Provider: "ollama", BaseURL: "http://127.0.0.1:11434/v1"}
	assert.NoError(t, provider.ValidateConnectionURL(c, provider.StrictURLPolicy))
}

func TestValidateConnectionURL_OllamaIPv6LoopbackHTTP_Allowed(t *testing.T) {
	c := provider.Connection{Name: "ollama-local", Provider: "ollama", BaseURL: "http://[::1]:11434/v1"}
	assert.NoError(t, provider.ValidateConnectionURL(c, provider.StrictURLPolicy))
}

func TestValidateConnectionURL_OllamaCloudHTTPS_Allowed(t *testing.T) {
	c := provider.Connection{Name: "ollama-cloud", Provider: "ollama", BaseURL: "https://ollama.com/v1"}
	assert.NoError(t, provider.ValidateConnectionURL(c, provider.StrictURLPolicy))
}

// Input-validation.

func TestValidateConnectionURL_OllamaProviderCaseAndWhitespace_Allowed(t *testing.T) {
	c := provider.Connection{Name: "ollama-local", Provider: "  Ollama  ", BaseURL: "http://localhost:11434"}
	assert.NoError(t, provider.ValidateConnectionURL(c, provider.StrictURLPolicy))
}

func TestValidateConnectionURL_OllamaEmptyBaseURL_Rejected(t *testing.T) {
	c := provider.Connection{Name: "ollama-empty", Provider: "ollama", BaseURL: ""}
	err := provider.ValidateConnectionURL(c, provider.StrictURLPolicy)
	assert.ErrorIs(t, err, provider.ErrInvalidBaseURL)
}

func TestValidateConnectionURL_OllamaUnparseableBaseURL_Rejected(t *testing.T) {
	c := provider.Connection{Name: "ollama-bad", Provider: "ollama", BaseURL: "http://%zz"}
	err := provider.ValidateConnectionURL(c, provider.StrictURLPolicy)
	assert.ErrorIs(t, err, provider.ErrInvalidBaseURL)
}

// Error-handling.

func TestValidateConnectionURL_OllamaPublicHTTP_Rejected(t *testing.T) {
	c := provider.Connection{Name: "ollama-pub", Provider: "ollama", BaseURL: "http://api.example.com/v1"}
	err := provider.ValidateConnectionURL(c, provider.StrictURLPolicy)
	assert.ErrorIs(t, err, provider.ErrInvalidBaseURL)
	assert.Contains(t, err.Error(), "ollama-pub")
}

func TestValidateConnectionURL_NilPolicyWithOllamaLoopback_Allowed(t *testing.T) {
	c := provider.Connection{Provider: "ollama", BaseURL: "http://localhost:11434"}
	assert.NoError(t, provider.ValidateConnectionURL(c, nil))
}

// Edge-cases.

func TestValidateConnectionURL_OllamaLoopback8HTTP_Allowed(t *testing.T) {
	c := provider.Connection{Name: "ollama-local", Provider: "ollama", BaseURL: "http://127.0.0.8:11434/v1"}
	assert.NoError(t, provider.ValidateConnectionURL(c, provider.StrictURLPolicy))
}

func TestValidateConnectionURL_OllamaUnspecified_Rejected(t *testing.T) {
	c := provider.Connection{Name: "ollama-zero", Provider: "ollama", BaseURL: "http://0.0.0.0:11434"}
	err := provider.ValidateConnectionURL(c, provider.StrictURLPolicy)
	assert.ErrorIs(t, err, provider.ErrInvalidBaseURL)
}

// Security: the load-bearing scoping checks that lock the carve-out's scope.

func TestValidateConnectionURL_NonOllamaLocalhost_StillRejected(t *testing.T) {
	for _, prov := range []string{"openrouter", "openai", "litellm", "azure-openai", ""} {
		c := provider.Connection{Name: "x", Provider: prov, BaseURL: "http://localhost:11434/v1"}
		err := provider.ValidateConnectionURL(c, provider.StrictURLPolicy)
		assert.ErrorIs(t, err, provider.ErrInvalidBaseURL, "provider %q must not earn loopback allowance", prov)
	}
}

func TestValidateConnectionURL_OllamaMetadataEndpoint_StillRejected(t *testing.T) {
	c := provider.Connection{Name: "ollama-meta", Provider: "ollama", BaseURL: "http://169.254.169.254/latest/meta-data/"}
	err := provider.ValidateConnectionURL(c, provider.StrictURLPolicy)
	assert.ErrorIs(t, err, provider.ErrInvalidBaseURL)
}

func TestValidateConnectionURL_OllamaLinkLocal_StillRejected(t *testing.T) {
	c := provider.Connection{Name: "ollama-ll", Provider: "ollama", BaseURL: "http://169.254.0.10/v1"}
	err := provider.ValidateConnectionURL(c, provider.StrictURLPolicy)
	assert.ErrorIs(t, err, provider.ErrInvalidBaseURL)
}

func TestValidateConnectionURL_OllamaRFC1918Hosts_StillRejected(t *testing.T) {
	for _, host := range []string{"10.0.0.5", "192.168.1.10", "172.16.0.1"} {
		c := provider.Connection{Name: "ollama-priv", Provider: "ollama", BaseURL: "http://" + host + "/v1"}
		err := provider.ValidateConnectionURL(c, provider.StrictURLPolicy)
		assert.ErrorIs(t, err, provider.ErrInvalidBaseURL, "rfc1918 host %q must be rejected", host)
	}
}
