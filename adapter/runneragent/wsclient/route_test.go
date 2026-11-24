package wsclient

import (
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestFromEnvelopeDispatchCarriesRoute(t *testing.T) {
	fp := strings.Repeat("a", 64)
	e, err := runnerproto.NewJobEnvelope(runnerproto.KindDispatch, tJob, 1, 0, runnerproto.Dispatch{Op: "chat", Chat: &runnerproto.WireChat{Model: "m", Prompt: "p", CredentialKind: "anthropic_subscription", Provider: "anthropic", EndpointFingerprint: fp}})
	require.NoError(t, err)
	in, err := fromEnvelope(e)
	require.NoError(t, err)
	assert.Equal(t, "anthropic", in.Dispatch.Chat.Provider)
	assert.Equal(t, fp, in.Dispatch.Chat.EndpointFingerprint)
}
func TestToEnvelopeHeartbeatCarriesAgeAndSourceEvidence(t *testing.T) {
	cap := runner.ExecutionCapability{Op: runner.OpChat, Provider: "anthropic", EndpointFingerprint: strings.Repeat("a", 64), Families: []modelidentity.Family{"anthropic"}, Models: []string{"claude-opus-5-5"}}
	env := roundTrip(t, runneragent.Outbound{Kind: runneragent.KindHeartbeat, Heartbeat: &runneragent.Heartbeat{TelemetryAgeMs: 1500, Sources: []runneragent.Source{{Kind: runner.CredAnthropicSubscription, UnavailableReason: "harness_unsupported", Capabilities: []runner.ExecutionCapability{cap}}}}})
	var hb runnerproto.Heartbeat
	require.NoError(t, runnerproto.DecodePayload(env, &hb))
	assert.Equal(t, int64(1500), hb.Telemetry.SampleAgeMs)
	require.Len(t, hb.Sources, 1)
	assert.Equal(t, "harness_unsupported", hb.Sources[0].UnavailableReason)
	require.Len(t, hb.Sources[0].Capabilities, 1)
	got := hb.Sources[0].Capabilities[0]
	assert.Equal(t, string(cap.Op), got.Op)
	assert.Equal(t, cap.Provider, got.Provider)
	assert.Equal(t, cap.EndpointFingerprint, got.EndpointFingerprint)
	assert.Equal(t, []string{"anthropic"}, got.Families)
	assert.Equal(t, cap.Models, got.Models)
}
