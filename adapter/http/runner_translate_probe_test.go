package http

import (
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func rtBool(v bool) *bool { return &v }

func TestRemoteProbe_NullFalseTrueAndModelScopesRoundTrip(t *testing.T) {
	reset := time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)
	head := 0.0
	for _, allowed := range []*bool{nil, rtBool(false), rtBool(true)} {
		reqID := uuid.New()
		wire := runnerproto.ProbeResult{RequestID: reqID.String(), HealthRevision: 9, Source: runnerproto.WireSource{
			Kind: "openai_subscription", CredentialRef: "acct", ObservationSeq: 4, SampleAgeMs: 25,
			Observation: &runnerproto.WireProbeObservation{HealthRevision: 9, Auth: "logged_out", Capacity: &runnerproto.WireCapacityObservation{
				ModelScope: "gpt-5.2-codex", State: "exhausted", OrdinaryUsageAllowed: allowed, ResetAt: &reset, Headroom: &head,
				Windows: []runnerproto.WireWindow{{LimitID: "weekly", ModelScope: "gpt-5.2-codex", DurationMins: 10080}}}},
			Health: runnerproto.WireHealth{Auth: "ok", Capacity: "available"}}}
		env, err := runnerproto.NewEnvelope(runnerproto.KindProbeResult, &wire)
		require.NoError(t, err)
		raw, err := runnerproto.Encode(env)
		require.NoError(t, err)
		decoded, err := runnerproto.Decode(raw)
		require.NoError(t, err)
		in, err := toInbound(decoded)
		require.NoError(t, err)
		require.Equal(t, runner.MsgProbeResult, in.Kind)
		rep := in.ProbeResult
		require.NotNil(t, rep)
		assert.Equal(t, reqID, rep.RequestID)
		assert.Equal(t, uint64(9), rep.HealthRevision)
		assert.Equal(t, runner.CredOpenAISubscription, rep.Source.Kind)
		assert.Equal(t, "acct", rep.Source.CredentialRef)
		assert.Equal(t, uint64(4), rep.Source.ObservationSeq)
		assert.Equal(t, int64(25), rep.Source.SampleAgeMs)
		obs := rep.Source.Observation
		require.NotNil(t, obs, "an observation is not collapsed into the health projection")
		assert.Equal(t, runner.AuthLoggedOut, obs.Auth)
		require.NotNil(t, obs.Capacity)
		assert.Equal(t, "gpt-5.2-codex", obs.Capacity.ModelScope)
		assert.Equal(t, allowed, obs.Capacity.OrdinaryUsageAllowed, "null/false/true survive")
		assert.True(t, obs.Capacity.ResetAt.Equal(reset))
		require.Len(t, obs.Capacity.Windows, 1)
		assert.Equal(t, "gpt-5.2-codex", obs.Capacity.Windows[0].ModelScope)
		assert.Equal(t, runner.AuthOK, rep.Source.Health.Auth, "the projection stays separate")
	}
	hb, err := runnerproto.NewEnvelope(runnerproto.KindHeartbeat, &runnerproto.Heartbeat{Sources: []runnerproto.WireSource{{Kind: "lunaroute", CredentialRef: "lr",
		Health: runnerproto.WireHealth{Auth: "logged_out"}}}})
	require.NoError(t, err)
	in, err := toInbound(hb)
	require.NoError(t, err)
	assert.Nil(t, in.Heartbeat.Sources[0].Observation, "a projection is never converted into an observation")
}

func TestFromOutbound_ProbeAndConfirmCarryCorrelationAndEvidence(t *testing.T) {
	reqID, jobID := uuid.New(), uuid.New()
	e, err := fromOutbound(runner.Outbound{Kind: runner.MsgProbe, Probe: &runner.ProbePayload{RequestID: reqID, HealthRevision: 7,
		SourceKind: runner.CredAnthropicSubscription}}, "mac-a", 5)
	require.NoError(t, err)
	var pr runnerproto.Probe
	require.NoError(t, runnerproto.DecodePayload(e, &pr))
	assert.Equal(t, runnerproto.Probe{RequestID: reqID.String(), HealthRevision: 7, SourceKind: "anthropic_subscription"}, pr)

	e, err = fromOutbound(runner.Outbound{Kind: runner.MsgDispatch, JobID: jobID, Generation: 1, Dispatch: &runner.DispatchPayload{Op: runner.OpConfirm,
		Confirmation: &runner.ConfirmationDispatch{Model: "claude-opus-5-5", Backend: "claude-code", Provider: "claude-cli", ContextWindow: "1m"}}}, "mac-a", 5)
	require.NoError(t, err)
	var d runnerproto.Dispatch
	require.NoError(t, runnerproto.DecodePayload(e, &d))
	assert.Equal(t, "confirm", d.Op)
	assert.Equal(t, &runnerproto.WireConfirmation{Model: "claude-opus-5-5", Backend: "claude-code", Provider: "claude-cli", ContextWindow: "1m"}, d.Confirmation)

	res := jobEnv(t, runnerproto.KindResult, jobID, 1, 0, &runnerproto.Result{OK: true, Outcome: runnerproto.WireOutcome{Class: ""},
		Confirmation: &runnerproto.WireConfirmationResult{RequestedModel: "claude-opus-5-5", ExecutedModel: "claude-opus-5-5", SessionID: "rs"}})
	in, err := toInbound(res)
	require.NoError(t, err)
	assert.Equal(t, &runner.ConfirmationResult{RequestedModel: "claude-opus-5-5", ExecutedModel: "claude-opus-5-5", SessionID: "rs"}, in.Result.Confirmation)
}
