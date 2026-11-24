package runneragent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
)

func TestMirrorsCarryRouteEvidence(t *testing.T) {
	cap := runner.ExecutionCapability{Op: runner.OpChat, Provider: "anthropic", EndpointFingerprint: "ab", Models: []string{"claude-opus-5-5"}}
	src := runneragent.Source{Kind: runner.CredAnthropicSubscription, UnavailableReason: "harness_unsupported", Capabilities: []runner.ExecutionCapability{cap}}
	assert.Equal(t, "harness_unsupported", src.UnavailableReason)
	assert.Equal(t, cap, src.Capabilities[0])
	job := runneragent.ChatJob{Model: "claude-opus-5-5", Provider: "anthropic", EndpointFingerprint: "ab"}
	assert.Equal(t, "ab", job.EndpointFingerprint)
	hb := runneragent.Heartbeat{TelemetryAgeMs: 1500}
	assert.Equal(t, int64(1500), hb.TelemetryAgeMs)
}
