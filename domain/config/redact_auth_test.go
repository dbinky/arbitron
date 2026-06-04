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
