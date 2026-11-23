package runner_test

import (
	"context"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHubAdmit_PreservesCapabilityEvidenceAndUnavailableReason(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	src := hubtest.AnthropicSource()
	src.UnavailableReason = "harness_unsupported"
	src.Capabilities = []runner.ExecutionCapability{{Op: runner.OpChat, Provider: "claude-cli", EndpointFingerprint: strings.Repeat("a", 64), Families: src.Families, Models: []string{"claude-opus-5-5"}, Tools: []string{"Read"}, ExecutedModel: true}}
	admit(t, p, h, "mac-a", hubtest.DarwinHello(src))
	_, sources, err := p.Registry.GetRunner(context.Background(), "mac-a")
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, src.UnavailableReason, sources[0].UnavailableReason)
	assert.Equal(t, src.Capabilities, sources[0].Capabilities)
}

func TestHubAdmit_RejectsUnknownCapabilityEvidence(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	key := p.SeedRunner(t, "mac-a", 2)
	src := hubtest.AnthropicSource()
	src.Capabilities = []runner.ExecutionCapability{{Op: "teleport"}}
	_, err := h.Admit(context.Background(), key, hubtest.NewFakeConn(), hubtest.DarwinHello(src))
	require.Error(t, err)
	_, sources, err := p.Registry.GetRunner(context.Background(), "mac-a")
	require.NoError(t, err)
	assert.Empty(t, sources)
}
