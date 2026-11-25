package main

import (
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildAgent_VerifiedConfirmationCapabilityReachesServer(t *testing.T) {
	got := capabilityRootRun(t, []string{"claude-sonnet-4-5", "claude-opus-5-5"}, nil)
	require.Len(t, got.heartbeat.Sources, 1)
	fp, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "claude-cli", Backend: "claude-code"})
	require.NoError(t, err)
	var count int
	for _, c := range got.heartbeat.Sources[0].Capabilities {
		if c.Op != string(runner.OpConfirm) {
			continue
		}
		count++
		assert.Equal(t, "claude-code", c.Backend)
		assert.Equal(t, fp, c.EndpointFingerprint)
		assert.Equal(t, []string{"claude-sonnet-4-5"}, c.Models, "substituted models must never reach the server as confirmation evidence")
		assert.True(t, c.ExecutedModel)
	}
	require.Equal(t, 1, count)
	require.True(t, got.result.OK)
}
