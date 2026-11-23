// adapter/http/runner_translate_test.go
package http

import (
	"encoding/json"
	"github.com/arbitron/arbitron/domain/provider"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
)

func jobEnv(t *testing.T, kind string, jobID uuid.UUID, gen int64, seq uint64, p any) runnerproto.Envelope {
	t.Helper()
	e, err := runnerproto.NewJobEnvelope(kind, jobID.String(), gen, seq, p)
	require.NoError(t, err)
	return e
}

func TestToInbound_Heartbeat(t *testing.T) {
	reset := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	cpu, head := 41.5, 0.25
	jobID := uuid.New()
	e, err := runnerproto.NewEnvelope(runnerproto.KindHeartbeat, &runnerproto.Heartbeat{
		Telemetry: runnerproto.WireTelemetry{SampledAt: reset, CPUPercent: &cpu},
		Sources: []runnerproto.WireSource{{Kind: "lunaroute", CredentialRef: "lunaroute", Families: []string{"zhipuai"},
			Health: runnerproto.WireHealth{Auth: "ok", Capacity: "exhausted", ResetAt: &reset, Headroom: &head,
				Windows: []runnerproto.WireWindow{{LimitID: "5h", DurationMins: 300}}}}},
		Jobs: []runnerproto.HeldJob{{JobID: jobID.String(), Generation: 2, State: "running", LastOutputSeq: 7}},
	})
	require.NoError(t, err)
	in, err := toInbound(e)
	require.NoError(t, err)
	assert.Equal(t, runner.MsgHeartbeat, in.Kind)
	require.NotNil(t, in.Heartbeat)
	assert.Equal(t, 41.5, *in.Heartbeat.Telemetry.CPUPercent)
	assert.True(t, in.Heartbeat.Telemetry.SampledAt.Equal(reset))
	require.Len(t, in.Heartbeat.Sources, 1)
	src := in.Heartbeat.Sources[0]
	assert.Equal(t, runner.CredLunaRoute, src.Kind)
	assert.Equal(t, []modelidentity.Family{"zhipuai"}, src.Families)
	assert.Equal(t, runner.AuthOK, src.Health.Auth)
	assert.Equal(t, runner.CapacityExhausted, src.Health.Capacity)
	assert.True(t, src.Health.ResetAt.Equal(reset))
	assert.Equal(t, []runner.LimitWindow{{LimitID: "5h", DurationMins: 300}}, src.Health.Windows)
	assert.Equal(t, []runner.HeldJobInfo{{JobID: jobID, Generation: 2, State: runner.HeldRunning, LastOutputSeq: 7}}, in.Heartbeat.Jobs)

	bare, err := runnerproto.NewEnvelope(runnerproto.KindHeartbeat, &runnerproto.Heartbeat{})
	require.NoError(t, err)
	in, err = toInbound(bare)
	require.NoError(t, err)
	assert.Nil(t, in.Heartbeat.Sources, "omitted sources mean unchanged")
}

func TestToInbound_JobMessages(t *testing.T) {
	id := uuid.New()
	in, err := toInbound(jobEnv(t, runnerproto.KindAccept, id, 3, 0, nil))
	require.NoError(t, err)
	assert.Equal(t, runner.Inbound{Kind: runner.MsgAccept, JobID: id, Generation: 3, Accept: true}, in)

	in, err = toInbound(jobEnv(t, runnerproto.KindReject, id, 3, 0, &runnerproto.Reject{Reason: "no source"}))
	require.NoError(t, err)
	assert.Equal(t, runner.Inbound{Kind: runner.MsgReject, JobID: id, Generation: 3, RejectReason: "no source"}, in)

	in, err = toInbound(jobEnv(t, runnerproto.KindOutput, id, 3, 5, &runnerproto.Output{Seq: 5, Token: "tok"}))
	require.NoError(t, err)
	assert.Equal(t, runner.Inbound{Kind: runner.MsgOutput, JobID: id, Generation: 3, Seq: 5, Output: &runner.OutputFrame{Seq: 5, Token: "tok"}}, in)

	_, err = toInbound(jobEnv(t, runnerproto.KindOutput, id, 3, 6, &runnerproto.Output{Seq: 5, Token: "tok"}))
	assert.ErrorIs(t, err, runner.ErrMalformedMessage, "envelope and payload sequence must agree")

	reset := time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)
	in, err = toInbound(jobEnv(t, runnerproto.KindResult, id, 3, 0, &runnerproto.Result{OK: false, ErrorText: "limit",
		Outcome: runnerproto.WireOutcome{Class: "exhausted", ResetAt: &reset, RetryAfterMs: 1500}}))
	require.NoError(t, err)
	require.NotNil(t, in.Result)
	assert.False(t, in.Result.OK)
	assert.Equal(t, "limit", in.Result.ErrorText)
	assert.Equal(t, runner.OutcomeExhausted, in.Result.Outcome.Class)
	assert.Equal(t, 1500*time.Millisecond, in.Result.Outcome.RetryAfter)
	assert.True(t, in.Result.Outcome.ResetAt.Equal(reset))

	in, err = toInbound(jobEnv(t, runnerproto.KindResult, id, 3, 0, &runnerproto.Result{OK: true,
		Chat: &runnerproto.WireChatResult{Output: "hi", Conversation: json.RawMessage(`[1]`), Model: "m", TokensIn: 1, TokensOut: 2, SessionID: "s"}}))
	require.NoError(t, err)
	assert.Equal(t, &runner.RemoteChatResult{Output: "hi", Conversation: json.RawMessage(`[1]`), Model: "m", TokensIn: 1, TokensOut: 2, SessionID: "s"}, in.Result.Chat)
}

func TestToInbound_UnexpectedKindsAreMalformed(t *testing.T) {
	for _, kind := range []string{runnerproto.KindHello, runnerproto.KindChallengeResponse, runnerproto.KindProbeResult} {
		e := runnerproto.Envelope{V: 1, Kind: kind}
		_, err := toInbound(e)
		assert.ErrorIs(t, err, runner.ErrMalformedMessage, kind)
	}
}

func TestFromOutbound(t *testing.T) {
	id := uuid.New()
	lease := time.Date(2026, 11, 20, 9, 2, 0, 0, time.UTC)

	e, err := fromOutbound(runner.Outbound{Kind: runner.MsgAdmitted}, "mac-a", 5)
	require.NoError(t, err)
	assert.Equal(t, runnerproto.KindAdmitted, e.Kind)
	var adm runnerproto.Admitted
	require.NoError(t, runnerproto.DecodePayload(e, &adm))
	assert.Equal(t, runnerproto.Admitted{RunnerID: "mac-a", HeartbeatSeconds: 5, ProtocolVersion: runnerproto.ProtocolVersion}, adm)

	e, err = fromOutbound(runner.Outbound{Kind: runner.MsgDispatch, JobID: id, Generation: 2, Dispatch: &runner.DispatchPayload{
		Op: runner.OpStream, LeaseUntil: lease, Chat: &runner.ChatDispatch{Model: "glm-5.3", SystemPrompt: "sys", Prompt: "p", MaxTokens: 9,
			Temperature: 0.5, OutputSchema: json.RawMessage(`{"type":"object"}`), SessionID: "s", RequireExecutedModel: true, Stream: true,
			CredentialKind: runner.CredLunaRoute, Backend: "", ContextWindow: "200k", Effort: "high", Tools: []string{"Read"}}}}, "mac-a", 5)
	require.NoError(t, err)
	assert.Equal(t, id.String(), e.JobID)
	assert.Equal(t, int64(2), e.Generation)
	var d runnerproto.Dispatch
	require.NoError(t, runnerproto.DecodePayload(e, &d))
	assert.Equal(t, "stream", d.Op)
	assert.True(t, d.LeaseUntil.Equal(lease))
	assert.Equal(t, runnerproto.WireChat{Model: "glm-5.3", SystemPrompt: "sys", Prompt: "p", MaxTokens: 9, Temperature: 0.5,
		OutputSchema: json.RawMessage(`{"type":"object"}`), SessionID: "s", RequireExecutedModel: true, Stream: true,
		CredentialKind: "lunaroute", ContextWindow: "200k", Effort: "high", Tools: []string{"Read"}}, *d.Chat)

	e, err = fromOutbound(runner.Outbound{Kind: runner.MsgAck, JobID: id, Generation: 2, Ack: &runner.AckPayload{Seq: 4, Result: true}}, "mac-a", 5)
	require.NoError(t, err)
	var ack runnerproto.Ack
	require.NoError(t, runnerproto.DecodePayload(e, &ack))
	assert.Equal(t, runnerproto.Ack{Seq: 4, Result: true}, ack)

	for kind, want := range map[string]string{runner.MsgFenced: runnerproto.KindFenced, runner.MsgCancel: runnerproto.KindCancel} {
		e, err = fromOutbound(runner.Outbound{Kind: kind, JobID: id, Generation: 2}, "mac-a", 5)
		require.NoError(t, err)
		assert.Equal(t, want, e.Kind)
		_, err = runnerproto.Encode(e)
		assert.NoError(t, err, "job-scoped frames are valid envelopes")
	}

	_, err = fromOutbound(runner.Outbound{Kind: runner.MsgDispatch, JobID: id, Generation: 2}, "mac-a", 5)
	assert.Error(t, err, "dispatch without payload")
	_, err = fromOutbound(runner.Outbound{Kind: "bogus"}, "mac-a", 5)
	assert.Error(t, err)
}

func TestHelloInfo(t *testing.T) {
	id := uuid.New()
	h, err := helloInfo(runnerproto.Hello{OS: "darwin", Arch: "arm64", ProtocolVersion: 1, RunnerVersion: "0.2.0",
		HarnessVersions: map[string]string{"claude": "2.1.30"}, WorkspaceRoot: "/srv/ws", ScratchRoot: "/scratch",
		Capabilities: []string{"chat"},
		Sources:      []runnerproto.WireSource{{Kind: "anthropic_subscription", Families: []string{"anthropic"}}},
		Held:         []runnerproto.HeldJob{{JobID: id.String(), Generation: 1, State: "terminal", LastOutputSeq: 3}}})
	require.NoError(t, err)
	assert.Equal(t, "darwin", h.OS)
	assert.Equal(t, 1, h.ProtocolVersion)
	assert.Equal(t, "/srv/ws", h.WorkspaceRoot)
	assert.Equal(t, "/scratch", h.ScratchRoot)
	require.Len(t, h.Sources, 1)
	assert.Equal(t, runner.AuthUnknown, h.Sources[0].Health.Auth, "an empty wire auth state is unknown")
	assert.Equal(t, runner.CapacityAvailable, h.Sources[0].Health.Capacity)
	assert.Equal(t, []runner.HeldJobInfo{{JobID: id, Generation: 1, State: runner.HeldTerminal, LastOutputSeq: 3}}, h.Held)

	_, err = helloInfo(runnerproto.Hello{OS: "darwin", ProtocolVersion: 1, RunnerVersion: "x",
		Held: []runnerproto.HeldJob{{JobID: "nope", Generation: 1, State: "running"}}})
	assert.ErrorIs(t, err, runner.ErrMalformedMessage)
}

func TestRunnerTranslate_SourceCapabilitiesAreLossless(t *testing.T) {
	src := runnerproto.WireSource{Kind: "anthropic_subscription", Families: []string{"anthropic"}, UnavailableReason: "harness_unsupported",
		Capabilities: []runnerproto.ExecutionCapability{{Op: "stream", Backend: "claude-code", Provider: "claude-cli", EndpointFingerprint: strings.Repeat("a", 64), Families: []string{"anthropic"}, Models: []string{"claude-opus-5-5"}, ContextWindows: []string{"1m"}, Efforts: []string{"high"}, Tools: []string{"Read"}, ExecutedModel: true}}}
	want := runner.SourceReport{Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"}, UnavailableReason: src.UnavailableReason,
		Health:       runner.SourceHealth{Auth: runner.AuthUnknown, Capacity: runner.CapacityAvailable},
		Capabilities: []runner.ExecutionCapability{{Op: runner.OpStream, Backend: "claude-code", Provider: "claude-cli", EndpointFingerprint: strings.Repeat("a", 64), Families: []modelidentity.Family{"anthropic"}, Models: []string{"claude-opus-5-5"}, ContextWindows: []provider.ContextWindow{"1m"}, Efforts: []provider.Effort{"high"}, Tools: []string{"Read"}, ExecutedModel: true}}}
	hello, err := helloInfo(runnerproto.Hello{Sources: []runnerproto.WireSource{src}})
	require.NoError(t, err)
	require.Equal(t, []runner.SourceReport{want}, hello.Sources)
	hb, err := heartbeatInfo(runnerproto.Heartbeat{Sources: []runnerproto.WireSource{src}})
	require.NoError(t, err)
	require.Equal(t, hello.Sources, hb.Sources)
}
