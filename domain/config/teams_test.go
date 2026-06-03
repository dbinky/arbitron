package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/config"
)

// Success: an absolute https URL with a public host is accepted.
func TestValidateTeamsWebhookURL_AcceptsPublicHTTPS(t *testing.T) {
	require.NoError(t, config.ValidateTeamsWebhookURL("https://example.com/webhook"))
	require.NoError(t, config.ValidateTeamsWebhookURL("https://prod-1.westus.logic.azure.com:443/workflows/abc?sig=xyz"))
	require.NoError(t, config.ValidateTeamsWebhookURL("https://8.8.8.8/hook"))
}

// Edge-case: the empty string is accepted because it clears the webhook.
func TestValidateTeamsWebhookURL_AcceptsEmpty(t *testing.T) {
	require.NoError(t, config.ValidateTeamsWebhookURL(""))
}

// Failure: a non-https scheme is rejected — Teams Workflows URLs are always
// https and plain http has no legitimate use here.
func TestValidateTeamsWebhookURL_RejectsNonHTTPS(t *testing.T) {
	for _, raw := range []string{
		"http://example.com/hook",
		"ftp://example.com/hook",
		"https:///nohost",
	} {
		assert.Error(t, config.ValidateTeamsWebhookURL(raw), "expected %q to be rejected", raw)
	}
}

// Error: a syntactically invalid URL is rejected.
func TestValidateTeamsWebhookURL_RejectsMalformed(t *testing.T) {
	assert.Error(t, config.ValidateTeamsWebhookURL("https://exa mple.com/hook"))
	assert.Error(t, config.ValidateTeamsWebhookURL("://missing-scheme"))
}

// Security: SSRF blocklist — loopback, link-local (incl. cloud metadata),
// unspecified, RFC1918, and ULA targets are all rejected, for both IP literals
// and the "localhost" name.
func TestValidateTeamsWebhookURL_RejectsSSRFTargets(t *testing.T) {
	for _, raw := range []string{
		"https://localhost/hook",
		"https://LOCALHOST/hook",
		"https://127.0.0.1/hook",
		"https://127.0.0.1:8443/hook",
		"https://[::1]/hook",
		"https://169.254.169.254/latest/meta-data", // cloud metadata
		"https://[fe80::1]/hook",                    // link-local IPv6
		"https://0.0.0.0/hook",                      // unspecified
		"https://10.0.0.5/hook",                     // RFC1918
		"https://172.16.0.1/hook",                   // RFC1918
		"https://192.168.1.1/hook",                  // RFC1918
		"https://[fc00::1]/hook",                    // ULA
	} {
		assert.Error(t, config.ValidateTeamsWebhookURL(raw), "expected SSRF target %q to be rejected", raw)
	}
}

// Success/Edge: redaction helper masks a non-empty secret and leaves empty
// empty, using the shared placeholder constant.
func TestRedactWebhookURL(t *testing.T) {
	assert.Equal(t, "", config.RedactWebhookURL(""))
	assert.Equal(t, config.RedactedSecret, config.RedactWebhookURL("https://secret.example.com/hook?sig=tok"))
	assert.Equal(t, "**REDACTED**", config.RedactedSecret)
}
