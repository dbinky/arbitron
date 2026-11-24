package runnerproto

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestWireChat_RouteFieldsValidate(t *testing.T) {
	c := WireChat{Model: "m", Prompt: "p", CredentialKind: "anthropic_subscription", Provider: "anthropic", EndpointFingerprint: strings.Repeat("a", 64)}
	require.NoError(t, c.Validate())
	c.EndpointFingerprint = "not-hex"
	assert.Error(t, c.Validate())
}
