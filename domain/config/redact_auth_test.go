package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SECURITY: the OIDC client secret must never appear in the redacted projection,
// only a boolean flag indicating whether OIDC is configured.
func TestRedact_NeverLeaksClientSecret(t *testing.T) {
	const secret = "super-secret-oidc-value-DO-NOT-LEAK"
	cfg := &Config{
		Auth: AuthConfig{
			Mode: AuthModeOrg,
			OIDC: OIDCConfig{ClientSecret: secret},
		},
	}
	red := Redact(cfg, TeamsConfig{})
	assert.Equal(t, AuthModeOrg, red.Auth.Mode)
	assert.True(t, red.Auth.OIDCConfigured)

	b, err := json.Marshal(red)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(b), secret),
		"redacted config JSON must never contain the OIDC client secret")
}

func TestRedact_LocalMode_OIDCNotConfigured(t *testing.T) {
	cfg := &Config{Auth: AuthConfig{Mode: AuthModeLocal}}
	red := Redact(cfg, TeamsConfig{})
	assert.Equal(t, AuthModeLocal, red.Auth.Mode)
	assert.False(t, red.Auth.OIDCConfigured)
}

// Partial OIDC (clientid/issuerurl set but secret absent) reports
// OIDCConfigured=false because without a secret the config is not operational.
func TestRedact_PartialOIDC_NotConfigured(t *testing.T) {
	cfg := &Config{
		Auth: AuthConfig{
			Mode: AuthModeOrg,
			OIDC: OIDCConfig{
				IssuerURL: "https://idp.example.com",
				ClientID:  "client-abc",
				// ClientSecret intentionally absent
			},
		},
	}
	red := Redact(cfg, TeamsConfig{})
	assert.Equal(t, AuthModeOrg, red.Auth.Mode)
	assert.False(t, red.Auth.OIDCConfigured,
		"OIDCConfigured must be false when ClientSecret is absent — a partial config is not operational")
}

// The redacted JSON must not contain any raw OIDC field values (clientid,
// issuerurl, redirecturl). RedactedAuth uses a positive allow-list so none of
// these fields are serializable, but this test guards against regression if the
// struct grows a new field that accidentally echoes a sensitive value.
func TestRedact_NeverLeaksAnyOIDCField(t *testing.T) {
	cfg := &Config{
		Auth: AuthConfig{
			Mode: AuthModeOrg,
			OIDC: OIDCConfig{
				IssuerURL:    "https://idp.example.com/SHOULDNOTAPPEAR",
				ClientID:     "client-SHOULDNOTAPPEAR",
				ClientSecret: "secret-SHOULDNOTAPPEAR",
				RedirectURL:  "https://app.example.com/SHOULDNOTAPPEAR/auth/callback",
			},
		},
	}
	red := Redact(cfg, TeamsConfig{})
	b, err := json.Marshal(red)
	require.NoError(t, err)
	body := string(b)

	for _, sentinel := range []string{"SHOULDNOTAPPEAR"} {
		assert.False(t, strings.Contains(body, sentinel),
			"redacted config JSON must not contain any raw OIDC field value")
	}
	// The boolean flag is present; the raw values are not.
	assert.True(t, red.Auth.OIDCConfigured)
}
