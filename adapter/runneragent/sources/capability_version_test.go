package sources

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildCapabilities_VersionRefusal(t *testing.T) {
	rules := provider.NewCapabilityRules(map[string]provider.BackendCapability{"claude-code": {}})
	for _, tc := range []struct{ version, reason string }{
		{"", "harness_version_unknown"},
		{"not-a-version", "harness_unsupported"},
		{"2.1.100", "harness_unsupported"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			caps, reason := BuildCapabilities(runner.CredAnthropicSubscription, tc.version, DiscoverInput{Rules: rules})
			assert.Empty(t, caps)
			assert.Equal(t, tc.reason, reason)
		})
	}
}

func TestDiscover_UnknownVersionIsUnavailable(t *testing.T) {
	r := &fakeRunner{answers: map[string]cmdAnswer{"--version": {lines: []string{"2.1.100 (Claude Code)"}}}}
	specs, harness := Discover(context.Background(), DiscoverInput{
		ClaudeBinary: "/local/claude", Runner: r,
		Rules: provider.NewCapabilityRules(map[string]provider.BackendCapability{"claude-code": {}}),
	})
	require.Len(t, specs, 1, "detected unsupported harness remains visible")
	assert.Equal(t, "2.1.100", harness["claude"])
	assert.Empty(t, specs[0].Source.Capabilities)
	assert.Equal(t, "harness_unsupported", specs[0].Source.UnavailableReason)
	inv := New(specs, harness, nil)
	got := inv.Sources(context.Background())
	require.Len(t, got, 1)
	assert.Equal(t, "harness_unsupported", got[0].UnavailableReason, "inventory must retain the refusal for hello")
	assert.Empty(t, got[0].Capabilities)
}
