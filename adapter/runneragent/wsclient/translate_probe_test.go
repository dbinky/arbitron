package wsclient

import (
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTranslate_ProbeReplyObservationAndConfirmRoundTrip(t *testing.T) {
	allowed := false
	env := roundTrip(t, runneragent.Outbound{Kind: runneragent.KindProbeResult, RequestID: "0f9d6a3e-8c1b-4b7e-9a2d-5e6f7a8b9c0d", HealthRevision: 3,
		ProbeResult: &runneragent.Source{Kind: runner.CredAnthropicSubscription, ObservationSeq: 12, SampleAgeMs: 40,
			Observation: &runner.ProbeResult{Auth: runner.AuthOK, Capacity: &runner.CapacityObservation{State: runner.CapacityExhausted,
				ModelScope: "claude-opus-5-5", OrdinaryUsageAllowed: &allowed}}}})
	var pr runnerproto.ProbeResult
	require.NoError(t, runnerproto.DecodePayload(env, &pr))
	assert.Equal(t, "0f9d6a3e-8c1b-4b7e-9a2d-5e6f7a8b9c0d", pr.RequestID)
	assert.Equal(t, uint64(3), pr.HealthRevision)
	assert.Equal(t, uint64(12), pr.Source.ObservationSeq)
	assert.Equal(t, int64(40), pr.Source.SampleAgeMs)
	require.NotNil(t, pr.Source.Observation)
	require.NotNil(t, pr.Source.Observation.Capacity)
	assert.Equal(t, &allowed, pr.Source.Observation.Capacity.OrdinaryUsageAllowed)

	in, err := fromEnvelope(mustEnv(t, runnerproto.KindDispatch, tJob, 2, runnerproto.Dispatch{Op: "confirm", Confirmation: &runnerproto.WireConfirmation{
		Model: "gpt-5.2-codex", Backend: "codex", Provider: "claude-cli", Effort: "high"}}))
	require.NoError(t, err)
	require.NotNil(t, in.Dispatch)
	assert.Nil(t, in.Dispatch.Chat)
	assert.Equal(t, &runneragent.ConfirmationJob{Model: "gpt-5.2-codex", Backend: "codex", Provider: "claude-cli", Effort: "high"}, in.Dispatch.Confirmation)

	env = roundTrip(t, runneragent.Outbound{Kind: runneragent.KindResult, JobID: tJob, Generation: 2, Result: &runneragent.Result{OK: true,
		Confirmation: &runner.ConfirmationResult{RequestedModel: "gpt-5.2-codex", ExecutedModel: "gpt-5.2-codex", SessionID: "th"}}})
	var r runnerproto.Result
	require.NoError(t, runnerproto.DecodePayload(env, &r))
	assert.Equal(t, &runnerproto.WireConfirmationResult{RequestedModel: "gpt-5.2-codex", ExecutedModel: "gpt-5.2-codex", SessionID: "th"}, r.Confirmation)
}
