package wsclient

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	tJob = "6f1c1f0e-8a39-4d0e-9d51-3a4c3c0b6a11"
	tKey = "0f8fad5b-d9cb-469f-a165-70867728950e"
)

func roundTrip(t *testing.T, m runneragent.Outbound) runnerproto.Envelope {
	t.Helper()
	env, err := toEnvelope(m)
	require.NoError(t, err)
	b, err := runnerproto.Encode(env)
	require.NoError(t, err)
	got, err := runnerproto.Decode(b)
	require.NoError(t, err)
	return got
}

func TestToEnvelopeHelloCarriesSourcesAndHeldJobs(t *testing.T) {
	probed := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	env := roundTrip(t, runneragent.Outbound{Kind: runneragent.KindHello, Hello: &runneragent.Hello{
		OS: "darwin", Arch: "arm64", ProtocolVersion: 1, RunnerVersion: "1.4.0",
		HarnessVersions: map[string]string{"claude": "2.1.400"},
		WorkspaceRoot:   "/srv/arbitron/workspaces", ScratchRoot: "/Users/op/.arbitron-runner/scratch",
		Capabilities: []string{"chat", "stream"},
		Sources: []runneragent.Source{{Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"}, Backends: []string{""},
			Health: runner.SourceHealth{Auth: "ok", LastProbeAt: &probed}}},
		Held: []runneragent.HeldJob{{JobID: tJob, Generation: 3, State: runneragent.HeldTerminal, LastOutputSeq: 9}},
	}})
	assert.Equal(t, runnerproto.KindHello, env.Kind)
	var h runnerproto.Hello
	require.NoError(t, runnerproto.DecodePayload(env, &h))
	assert.Equal(t, "darwin", h.OS)
	assert.Equal(t, "2.1.400", h.HarnessVersions["claude"])
	assert.Equal(t, "/srv/arbitron/workspaces", h.WorkspaceRoot)
	require.Len(t, h.Sources, 1)
	assert.Equal(t, "anthropic_subscription", h.Sources[0].Kind)
	assert.Equal(t, []string{"anthropic"}, h.Sources[0].Families)
	assert.Equal(t, "ok", h.Sources[0].Health.Auth)
	assert.Equal(t, &probed, h.Sources[0].Health.LastProbeAt)
	assert.Equal(t, []runnerproto.HeldJob{{JobID: tJob, Generation: 3, State: "terminal", LastOutputSeq: 9}}, h.Held)
}

func TestToEnvelopeHeartbeatCarriesTelemetry(t *testing.T) {
	cpu, cores := 42.5, 10
	at := time.Date(2026, 11, 20, 9, 0, 5, 0, time.UTC)
	env := roundTrip(t, runneragent.Outbound{Kind: runneragent.KindHeartbeat, Heartbeat: &runneragent.Heartbeat{
		Telemetry: runner.Telemetry{SampledAt: at, CPUPercent: &cpu, Cores: &cores},
	}})
	var hb runnerproto.Heartbeat
	require.NoError(t, runnerproto.DecodePayload(env, &hb))
	assert.Equal(t, at, hb.Telemetry.SampledAt)
	assert.Equal(t, &cpu, hb.Telemetry.CPUPercent)
	assert.Equal(t, &cores, hb.Telemetry.Cores)
	assert.Nil(t, hb.Telemetry.Thermal, "unavailable stays unavailable on the wire")
}

func TestToEnvelopeChallengeResponseIsBase64(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	nonce := make([]byte, 32)
	sig := ed25519.Sign(priv, runneragent.ChallengeMessage(tKey, nonce))
	env := roundTrip(t, runneragent.Outbound{Kind: runneragent.KindChallengeResponse, ChallengeResponse: &runneragent.ChallengeResponse{PublicKey: pub, Signature: sig}})
	var cr runnerproto.ChallengeResponse
	require.NoError(t, runnerproto.DecodePayload(env, &cr))
	gotPub, gotSig, err := cr.Decoded()
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(gotPub, runneragent.ChallengeMessage(tKey, nonce), gotSig))
}

func TestToEnvelopeJobFrames(t *testing.T) {
	acc := roundTrip(t, runneragent.Outbound{Kind: runneragent.KindAccept, JobID: tJob, Generation: 2})
	assert.Equal(t, runnerproto.KindAccept, acc.Kind)
	assert.Equal(t, tJob, acc.JobID)
	assert.Equal(t, int64(2), acc.Generation)

	rej := roundTrip(t, runneragent.Outbound{Kind: runneragent.KindReject, JobID: tJob, Generation: 2, RejectReason: "no LunaRoute key"})
	var r runnerproto.Reject
	require.NoError(t, runnerproto.DecodePayload(rej, &r))
	assert.Equal(t, "no LunaRoute key", r.Reason)

	out := roundTrip(t, runneragent.Outbound{Kind: runneragent.KindOutput, JobID: tJob, Generation: 2, Seq: 7, Output: &runneragent.OutputFrame{Seq: 7, Token: "hi"}})
	var o runnerproto.Output
	require.NoError(t, runnerproto.DecodePayload(out, &o))
	assert.Equal(t, uint64(7), out.Seq)
	assert.Equal(t, runnerproto.Output{Seq: 7, Token: "hi"}, o)
}

func TestToEnvelopeResultOKAndFailure(t *testing.T) {
	ok := roundTrip(t, runneragent.Outbound{Kind: runneragent.KindResult, JobID: tJob, Generation: 1, Result: &runneragent.Result{
		OK: true, Chat: &runneragent.ChatResult{Output: "done", Model: "claude-sonnet-4-5", SessionID: "s1", TokensIn: 3, TokensOut: 1},
	}})
	var r runnerproto.Result
	require.NoError(t, runnerproto.DecodePayload(ok, &r))
	require.NotNil(t, r.Chat)
	assert.True(t, r.OK)
	assert.Equal(t, runnerproto.WireChatResult{Output: "done", Model: "claude-sonnet-4-5", SessionID: "s1", TokensIn: 3, TokensOut: 1}, *r.Chat)

	reset := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	used := 100.0
	fail := roundTrip(t, runneragent.Outbound{Kind: runneragent.KindResult, JobID: tJob, Generation: 1, Result: &runneragent.Result{
		OK: false, ErrorText: "harness process failed",
		Outcome: runner.SourceOutcome{Class: runner.OutcomeExhausted, ResetAt: &reset, RetryAfter: 1500 * time.Millisecond,
			Windows: []runner.LimitWindow{{LimitID: "five_hour", DurationMins: 300, UsedPercent: &used, ResetAt: &reset}}},
	}})
	var f runnerproto.Result
	require.NoError(t, runnerproto.DecodePayload(fail, &f))
	assert.False(t, f.OK)
	assert.Nil(t, f.Chat)
	assert.Equal(t, "exhausted", f.Outcome.Class)
	assert.Equal(t, &reset, f.Outcome.ResetAt)
	assert.Equal(t, int64(1500), f.Outcome.RetryAfterMs)
	require.Len(t, f.Outcome.Windows, 1)
	assert.Equal(t, "five_hour", f.Outcome.Windows[0].LimitID)
}

func TestToEnvelopeProbeResult(t *testing.T) {
	env := roundTrip(t, runneragent.Outbound{Kind: runneragent.KindProbeResult, RequestID: "0f9d6a3e-8c1b-4b7e-9a2d-5e6f7a8b9c0d", HealthRevision: 3, ProbeResult: &runneragent.Source{Kind: runner.CredLunaRoute, CredentialRef: "lr-main", Health: runner.SourceHealth{Auth: "logged_out"}}})
	var pr runnerproto.ProbeResult
	require.NoError(t, runnerproto.DecodePayload(env, &pr))
	assert.Equal(t, "0f9d6a3e-8c1b-4b7e-9a2d-5e6f7a8b9c0d", pr.RequestID)
	assert.Equal(t, uint64(3), pr.HealthRevision)
	assert.Equal(t, "lunaroute", pr.Source.Kind)
	assert.Equal(t, "logged_out", pr.Source.Health.Auth)
}

func mustEnv(t *testing.T, kind, job string, gen int64, payload any) runnerproto.Envelope {
	t.Helper()
	e, err := runnerproto.NewJobEnvelope(kind, job, gen, 0, payload)
	require.NoError(t, err)
	return e
}

func TestFromEnvelopeChallengeDecodesNonce(t *testing.T) {
	nonce := []byte("0123456789abcdef0123456789abcdef")
	in, err := fromEnvelope(mustEnv(t, runnerproto.KindChallenge, "", 0, runnerproto.Challenge{KeyID: tKey, Nonce: base64.StdEncoding.EncodeToString(nonce)}))
	require.NoError(t, err)
	require.NotNil(t, in.Challenge)
	assert.Equal(t, tKey, in.Challenge.KeyID)
	assert.Equal(t, nonce, in.Challenge.Nonce)
}

func TestFromEnvelopeAdmittedRefusedAck(t *testing.T) {
	in, err := fromEnvelope(mustEnv(t, runnerproto.KindAdmitted, "", 0, runnerproto.Admitted{RunnerID: "mac-studio", HeartbeatSeconds: 5, ProtocolVersion: 1}))
	require.NoError(t, err)
	assert.Equal(t, &runneragent.Admission{RunnerID: "mac-studio", HeartbeatSeconds: 5}, in.Admitted)

	in, err = fromEnvelope(mustEnv(t, runnerproto.KindRefused, "", 0, runnerproto.Refused{Code: "revoked", Reason: "key revoked"}))
	require.NoError(t, err)
	assert.Equal(t, &runneragent.Refusal{Code: "revoked", Reason: "key revoked"}, in.Refused)

	in, err = fromEnvelope(mustEnv(t, runnerproto.KindAck, tJob, 4, runnerproto.Ack{Seq: 12, Result: true}))
	require.NoError(t, err)
	assert.Equal(t, runneragent.Inbound{Kind: runneragent.KindAck, JobID: tJob, Generation: 4, Seq: 12, AckResult: true}, in)
}

func TestFromEnvelopeDispatchChat(t *testing.T) {
	lease := time.Date(2026, 11, 20, 9, 1, 0, 0, time.UTC)
	in, err := fromEnvelope(mustEnv(t, runnerproto.KindDispatch, tJob, 2, runnerproto.Dispatch{Op: "chat", LeaseUntil: lease, Chat: &runnerproto.WireChat{
		Model: "claude-sonnet-4-5", SystemPrompt: "sys", Prompt: "hi", MaxTokens: 100, Temperature: 0.2,
		OutputSchema: json.RawMessage(`{"type":"object"}`), RequireExecutedModel: true, Stream: true,
		CredentialKind: "anthropic_subscription", ContextWindow: "200k", Effort: "high",
	}}))
	require.NoError(t, err)
	require.NotNil(t, in.Dispatch)
	assert.Equal(t, runneragent.Dispatch{JobID: tJob, Generation: 2, Op: "chat", LeaseUntil: lease, Chat: &runneragent.ChatJob{
		Model: "claude-sonnet-4-5", SystemPrompt: "sys", Prompt: "hi", MaxTokens: 100, Temperature: 0.2,
		OutputSchema: json.RawMessage(`{"type":"object"}`), RequireExecutedModel: true, Stream: true,
		CredentialKind: "anthropic_subscription", ContextWindow: "200k", Effort: "high",
	}}, *in.Dispatch)
}

func TestFromEnvelopeCancelFencedProbe(t *testing.T) {
	in, err := fromEnvelope(mustEnv(t, runnerproto.KindFenced, tJob, 3, nil))
	require.NoError(t, err)
	assert.Equal(t, runneragent.Inbound{Kind: runneragent.KindFenced, JobID: tJob, Generation: 3}, in)

	in, err = fromEnvelope(mustEnv(t, runnerproto.KindCancel, tJob, 3, runnerproto.Cancel{Reason: "run cancelled"}))
	require.NoError(t, err)
	assert.Equal(t, runneragent.KindCancel, in.Kind)

	in, err = fromEnvelope(mustEnv(t, runnerproto.KindProbe, "", 0, runnerproto.Probe{RequestID: "0f9d6a3e-8c1b-4b7e-9a2d-5e6f7a8b9c0d", HealthRevision: 3, SourceKind: "lunaroute", CredentialRef: "lr-main"}))
	require.NoError(t, err)
	assert.Equal(t, &runneragent.ProbeRequest{RequestID: "0f9d6a3e-8c1b-4b7e-9a2d-5e6f7a8b9c0d", HealthRevision: 3, SourceKind: runner.CredLunaRoute, CredentialRef: "lr-main"}, in.Probe)
}

func TestFromEnvelopeRejectsInvalidPayload(t *testing.T) {
	_, err := fromEnvelope(mustEnv(t, runnerproto.KindChallenge, "", 0, runnerproto.Challenge{KeyID: "not-a-uuid", Nonce: "x"}))
	assert.ErrorIs(t, err, runnerproto.ErrMalformed)
	_, err = fromEnvelope(mustEnv(t, runnerproto.KindDispatch, tJob, 1, runnerproto.Dispatch{Op: "chat", Chat: &runnerproto.WireChat{Prompt: "no model", CredentialKind: "anthropic_subscription"}}))
	assert.ErrorIs(t, err, runnerproto.ErrMalformed)
}

func TestToEnvelopeRejectCarriesCode(t *testing.T) {
	rej := roundTrip(t, runneragent.Outbound{Kind: runneragent.KindReject, JobID: tJob, Generation: 2,
		RejectReason: "jq missing", RejectCode: "command_unavailable"})
	var r runnerproto.Reject
	require.NoError(t, runnerproto.DecodePayload(rej, &r))
	assert.Equal(t, "jq missing", r.Reason)
	assert.Equal(t, "command_unavailable", r.Code)
}

func TestFromEnvelopeAdmittedCarriesClaudeModels(t *testing.T) {
	env, err := runnerproto.NewEnvelope(runnerproto.KindAdmitted, &runnerproto.Admitted{RunnerID: "mac-1", HeartbeatSeconds: 5, ClaudeModels: []string{"claude-opus-5-5"}})
	require.NoError(t, err)
	in, err := fromEnvelope(env)
	require.NoError(t, err)
	require.NotNil(t, in.Admitted)
	assert.Equal(t, []string{"claude-opus-5-5"}, in.Admitted.ClaudeModels)
}
