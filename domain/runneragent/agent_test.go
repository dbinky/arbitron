package runneragent_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
)

func TestNewRejectsMissingPorts(t *testing.T) {
	_, err := runneragent.New(testConfig(), runneragent.Deps{})
	require.Error(t, err)
	for _, name := range []string{"Conn", "Exec", "Sources", "Telemetry", "Spool", "Identity"} {
		assert.Contains(t, err.Error(), name)
	}
}

func TestAgentAdmissionSignsChallengeAndAdvertises(t *testing.T) {
	h := newHarness(t, 64)
	h.start()
	s := h.session()
	hello := admit(t, s, h.key.Public().(ed25519.PublicKey))

	assert.Equal(t, "darwin", hello.OS)
	assert.Equal(t, "arm64", hello.Arch)
	assert.Equal(t, 1, hello.ProtocolVersion)
	assert.Equal(t, "1.4.0", hello.RunnerVersion)
	assert.Equal(t, map[string]string{"claude": "2.1.3"}, hello.HarnessVersions)
	assert.Equal(t, "/srv/arbitron/workspaces", hello.WorkspaceRoot)
	assert.Equal(t, []string{runneragent.OpChat}, hello.Capabilities)
	require.Len(t, hello.Sources, 1)
	assert.Equal(t, runner.CredAnthropicSubscription, hello.Sources[0].Kind)
	assert.Equal(t, "probe-1", hello.Sources[0].Health.Detail, "the startup probe ran before hello")
	assert.Empty(t, hello.Held)
}

func TestAgentReconnectBackoffIsJitteredExponentialCappedAt30s(t *testing.T) {
	dialErr := errors.New("connection refused")
	h := newHarness(t, 64, withDialErrors(dialErr, dialErr, dialErr, dialErr, dialErr, dialErr, dialErr, dialErr))
	mark := h.clock.mark()
	h.start()
	want := []time.Duration{
		500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second,
		8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	m := mark
	for _, d := range want {
		m = h.clock.awaitAndAdvance(t, m, d)
	}
	s := h.admitted()
	assert.Equal(t, want, h.clock.requestedSince(mark, time.Hour, 5*time.Second), "probe-interval and heartbeat waits are excluded")
	assert.Equal(t, 9, h.conn.dialCount())

	// A successful admission resets the attempt counter.
	m2 := h.clock.mark()
	require.NoError(t, s.Close())
	h.clock.awaitAndAdvance(t, m2, 500*time.Millisecond)
	h.admitted()
}

func TestAgentRefusedAdmissionBacksOffAndRetries(t *testing.T) {
	h := newHarness(t, 64)
	mark := h.clock.mark()
	h.start()
	s := h.session()
	s.push(runneragent.Inbound{Kind: runneragent.KindChallenge, Challenge: &runneragent.Challenge{KeyID: testKeyID, Nonce: []byte("n")}})
	s.expect(t, runneragent.KindChallengeResponse)
	s.expect(t, runneragent.KindHello)
	s.push(runneragent.Inbound{Kind: runneragent.KindRefused, Refused: &runneragent.Refusal{Code: "wrong_machine", Reason: "signature mismatch"}})
	h.clock.awaitAndAdvance(t, mark, 500*time.Millisecond)
	h.admitted()
	assert.Equal(t, 2, h.conn.dialCount())
}

func TestAgentHeartbeatGapUnderIntervalSendsNothing(t *testing.T) {
	h := newHarness(t, 64)
	h.start()
	s := h.admitted()
	h.clock.Advance(4 * time.Second)
	s.none(t, runneragent.KindHeartbeat, 100*time.Millisecond)
}

// Contracts §21 freshness: the agent reports the sample's monotonic age so the
// hub can normalize SampledAt to its own receipt time (P2 T8 Cycle 2b seam).
func TestAgentHeartbeatReportsTelemetrySampleAge(t *testing.T) {
	h := newHarness(t, 64)
	h.tel.lag = 1500 * time.Millisecond
	h.start()
	mark := h.clock.mark()
	s := h.admitted()
	h.clock.awaitAndAdvance(t, mark, 5*time.Second)
	hb := s.expect(t, runneragent.KindHeartbeat)
	assert.Equal(t, int64(1500), hb.Heartbeat.TelemetryAgeMs)
}

func TestAgentHeartbeatTelemetryErrorReportsUnavailable(t *testing.T) {
	h := newHarness(t, 64)
	h.tel.err = errors.New("sysctl failed")
	h.start()
	mark := h.clock.mark()
	s := h.admitted()
	h.clock.awaitAndAdvance(t, mark, 5*time.Second)
	hb := s.expect(t, runneragent.KindHeartbeat)
	assert.Equal(t, runner.Telemetry{SampledAt: h.clock.Now()}, hb.Heartbeat.Telemetry, "a failed sample is reported as unavailable, never fabricated")
}

func TestAgentProbeRequestRefreshesAndAnswersForThatSource(t *testing.T) {
	h := newHarness(t, 64)
	h.start()
	s := h.admitted()
	before := h.src.probeCount()
	s.push(runneragent.Inbound{Kind: runneragent.KindProbe, Probe: &runneragent.ProbeRequest{SourceKind: runner.CredAnthropicSubscription}})
	pr := s.expect(t, runneragent.KindProbeResult)
	require.NotNil(t, pr.ProbeResult)
	assert.Equal(t, before+1, h.src.probeCount())
	assert.Equal(t, runner.CredAnthropicSubscription, pr.ProbeResult.Kind)
	assert.Equal(t, "probe-"+fmt.Sprint(before+1), pr.ProbeResult.Health.Detail, "the answer is the freshly probed health")

	s.push(runneragent.Inbound{Kind: runneragent.KindProbe, Probe: &runneragent.ProbeRequest{SourceKind: runner.CredLunaRoute, CredentialRef: "lunaroute"}})
	missing := s.expect(t, runneragent.KindProbeResult)
	assert.Equal(t, runner.CredLunaRoute, missing.ProbeResult.Kind)
	assert.Equal(t, runner.AuthState("unknown"), missing.ProbeResult.Health.Auth, "an absent source is never reported healthy")
}

func TestAgentUsesServerHeartbeatInterval(t *testing.T) {
	h := newHarness(t, 64)
	h.start()
	mark := h.clock.mark()
	s := h.session()
	nonce := []byte("0123456789abcdef0123456789abcdef")
	s.push(runneragent.Inbound{Kind: runneragent.KindChallenge, Challenge: &runneragent.Challenge{KeyID: testKeyID, Nonce: nonce}})
	s.expect(t, runneragent.KindChallengeResponse)
	s.expect(t, runneragent.KindHello)
	s.push(runneragent.Inbound{Kind: runneragent.KindAdmitted, Admitted: &runneragent.Admission{RunnerID: "mac-studio", HeartbeatSeconds: 7}})
	h.clock.awaitAndAdvance(t, mark, 7*time.Second)
	s.expect(t, runneragent.KindHeartbeat)
}

// atomic is used by the backpressure test appended in Cycle 7.
var _ atomic.Int32

func TestAgentHeartbeatCarriesTelemetrySourcesAndJobs(t *testing.T) {
	h := newHarness(t, 64)
	hold := make(chan struct{})
	ref := runneragent.JobRef{JobID: "job-1", Generation: 1}
	h.exec.script(ref, func(ctx context.Context, d runneragent.Dispatch, emit runneragent.EmitFunc) runneragent.Result {
		<-hold
		return runneragent.Result{OK: true, Chat: &runneragent.ChatResult{Output: "x"}}
	})
	t.Cleanup(func() { close(hold) })
	h.start()
	mark := h.clock.mark()
	s := h.admitted()
	s.push(dispatch(ref, string(runner.CredAnthropicSubscription)))
	s.expect(t, runneragent.KindAccept)

	next := h.clock.awaitAndAdvance(t, mark, 5*time.Second)
	hb1 := s.expect(t, runneragent.KindHeartbeat)
	require.NotNil(t, hb1.Heartbeat)
	require.NotNil(t, hb1.Heartbeat.Telemetry.CPUPercent)
	assert.Equal(t, 10.0, *hb1.Heartbeat.Telemetry.CPUPercent)
	assert.Equal(t, h.clock.Now(), hb1.Heartbeat.Telemetry.SampledAt)
	require.Len(t, hb1.Heartbeat.Sources, 1)
	assert.Equal(t, []runneragent.HeldJob{{JobID: "job-1", Generation: 1, State: runneragent.HeldRunning}}, hb1.Heartbeat.Jobs)

	h.clock.awaitAndAdvance(t, next, 5*time.Second)
	hb2 := s.expect(t, runneragent.KindHeartbeat)
	assert.Equal(t, 20.0, *hb2.Heartbeat.Telemetry.CPUPercent, "each heartbeat samples telemetry afresh")
}

func TestAgentIgnoresMalformedServerMessages(t *testing.T) {
	h := newHarness(t, 64)
	h.start()
	s := h.admitted()
	s.push(runneragent.Inbound{Kind: "bogus"})
	s.push(runneragent.Inbound{Kind: runneragent.KindDispatch, JobID: "job-x", Generation: 1})
	ref := runneragent.JobRef{JobID: "job-2", Generation: 1}
	s.push(dispatch(ref, string(runner.CredAnthropicSubscription)))
	acc := s.expect(t, runneragent.KindAccept)
	assert.Equal(t, "job-2", acc.JobID, "the session survives malformed messages")
	assert.Eventually(t, func() bool { return h.exec.callCount() == 1 }, 5*time.Second, 5*time.Millisecond, "the accepted job reaches the executor")
	assert.Equal(t, 1, h.exec.callCount(), "exactly once")
}

func TestAgentStreamsOutputInSequenceThenResult(t *testing.T) {
	h := newHarness(t, 64)
	ref := runneragent.JobRef{JobID: "job-1", Generation: 3}
	h.exec.script(ref, func(ctx context.Context, d runneragent.Dispatch, emit runneragent.EmitFunc) runneragent.Result {
		require.NoError(t, emit(ctx, "he"))
		require.NoError(t, emit(ctx, "llo"))
		return runneragent.Result{OK: true, Chat: &runneragent.ChatResult{Output: "hello", Model: d.Chat.Model, TokensIn: 3, TokensOut: 2}}
	})
	h.start()
	s := h.admitted()
	s.push(dispatch(ref, string(runner.CredAnthropicSubscription)))
	acc := s.expect(t, runneragent.KindAccept)
	assert.Equal(t, int64(3), acc.Generation)
	o1 := s.expect(t, runneragent.KindOutput)
	o2 := s.expect(t, runneragent.KindOutput)
	assert.Equal(t, runneragent.OutputFrame{Seq: 1, Token: "he"}, *o1.Output)
	assert.Equal(t, runneragent.OutputFrame{Seq: 2, Token: "llo"}, *o2.Output)
	res := s.expect(t, runneragent.KindResult)
	require.True(t, res.Result.OK)
	assert.Equal(t, "hello", res.Result.Chat.Output)
	assert.Equal(t, int64(3), res.Generation)
	require.Len(t, h.exec.calls, 1)
	assert.Equal(t, "claude-sonnet-4-5", h.exec.calls[0].Chat.Model, "the executor receives the dispatched descriptor")
}

func TestAgentDuplicateDispatchIsNoop(t *testing.T) {
	h := newHarness(t, 64)
	hold := make(chan struct{})
	ref := runneragent.JobRef{JobID: "job-1", Generation: 1}
	h.exec.script(ref, func(ctx context.Context, d runneragent.Dispatch, emit runneragent.EmitFunc) runneragent.Result {
		<-hold
		return runneragent.Result{OK: true, Chat: &runneragent.ChatResult{Output: "x"}}
	})
	h.start()
	s := h.admitted()
	s.push(dispatch(ref, string(runner.CredAnthropicSubscription)))
	s.expect(t, runneragent.KindAccept)
	s.push(dispatch(ref, string(runner.CredAnthropicSubscription)))
	s.expect(t, runneragent.KindAccept)
	close(hold)
	s.expect(t, runneragent.KindResult)
	s.none(t, runneragent.KindResult, 100*time.Millisecond)
	assert.Equal(t, 1, h.exec.callCount(), "a redelivered (job, generation) never executes twice")
}

func TestAgentRejectsDispatchWithoutCapability(t *testing.T) {
	h := newHarness(t, 64)
	h.exec.unsupported[string(runner.CredOpenAISubscription)] = "no openai_subscription chat on this runner"
	h.start()
	s := h.admitted()
	s.push(dispatch(runneragent.JobRef{JobID: "job-o", Generation: 1}, string(runner.CredOpenAISubscription)))
	rej := s.expect(t, runneragent.KindReject)
	assert.Equal(t, "job-o", rej.JobID)
	assert.Equal(t, "no openai_subscription chat on this runner", rej.RejectReason)

	s.push(runneragent.Inbound{Kind: runneragent.KindDispatch, JobID: "job-a", Generation: 1,
		Dispatch: &runneragent.Dispatch{Op: "agent"}})
	rej2 := s.expect(t, runneragent.KindReject)
	assert.Equal(t, "unsupported op agent", rej2.RejectReason)
	assert.Equal(t, 0, h.exec.callCount())
}

func TestAgentNewerGenerationSupersedesOlder(t *testing.T) {
	h := newHarness(t, 64)
	gen1 := runneragent.JobRef{JobID: "job-1", Generation: 1}
	gen2 := runneragent.JobRef{JobID: "job-1", Generation: 2}
	killed := make(chan error, 1)
	h.exec.script(gen1, func(ctx context.Context, d runneragent.Dispatch, emit runneragent.EmitFunc) runneragent.Result {
		<-ctx.Done()
		killed <- ctx.Err()
		return runneragent.Result{OK: true, Chat: &runneragent.ChatResult{Output: "stale"}}
	})
	h.start()
	s := h.admitted()
	s.push(dispatch(gen1, string(runner.CredAnthropicSubscription)))
	s.expect(t, runneragent.KindAccept)
	s.push(dispatch(gen2, string(runner.CredAnthropicSubscription)))
	acc := s.expect(t, runneragent.KindAccept)
	assert.Equal(t, int64(2), acc.Generation)
	select {
	case err := <-killed:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("superseded generation was not killed")
	}
	out := s.expect(t, runneragent.KindOutput)
	assert.Equal(t, int64(2), out.Generation)
	res := s.expect(t, runneragent.KindResult)
	assert.Equal(t, int64(2), res.Generation, "only the newer generation reports")
	s.none(t, runneragent.KindResult, 100*time.Millisecond)
}

func TestAgentReconnectResendsUnackedOutputWithoutGapsOrDuplicates(t *testing.T) {
	h := newHarness(t, 64)
	ref := runneragent.JobRef{JobID: "job-1", Generation: 1}
	more := make(chan struct{})
	h.exec.script(ref, func(ctx context.Context, d runneragent.Dispatch, emit runneragent.EmitFunc) runneragent.Result {
		for _, tok := range []string{"t1", "t2", "t3"} {
			if err := emit(ctx, tok); err != nil {
				return runneragent.Result{ErrorText: err.Error()}
			}
		}
		<-more
		for _, tok := range []string{"t4", "t5"} {
			if err := emit(ctx, tok); err != nil {
				return runneragent.Result{ErrorText: err.Error()}
			}
		}
		return runneragent.Result{OK: true, Chat: &runneragent.ChatResult{Output: "t1t2t3t4t5"}}
	})
	h.start()
	s1 := h.admitted()
	s1.push(dispatch(ref, string(runner.CredAnthropicSubscription)))
	s1.expect(t, runneragent.KindAccept)
	for i := uint64(1); i <= 3; i++ {
		require.Equal(t, i, s1.expect(t, runneragent.KindOutput).Seq)
	}
	s1.push(runneragent.Inbound{Kind: runneragent.KindAck, JobID: "job-1", Generation: 1, Seq: 2})
	require.Eventually(t, func() bool {
		sj, ok, _ := h.spool.Get(context.Background(), ref)
		return ok && sj.LastAcked == 2
	}, 5*time.Second, time.Millisecond)

	mark := h.clock.mark()
	require.NoError(t, s1.Close()) // socket drop; frame 3 was sent but never acknowledged
	close(more)                    // the job keeps running while disconnected
	require.Eventually(t, func() bool {
		sj, ok, _ := h.spool.Get(context.Background(), ref)
		return ok && sj.Result != nil
	}, 5*time.Second, time.Millisecond)

	h.clock.awaitAndAdvance(t, mark, 500*time.Millisecond)
	s2 := h.session()
	hello := admit(t, s2, h.key.Public().(ed25519.PublicKey))
	assert.Equal(t, []runneragent.HeldJob{{JobID: "job-1", Generation: 1, State: runneragent.HeldTerminal, LastOutputSeq: 5}}, hello.Held)

	var seqs []uint64
	var toks []string
	for i := 0; i < 3; i++ {
		o := s2.expect(t, runneragent.KindOutput)
		seqs = append(seqs, o.Seq)
		toks = append(toks, o.Output.Token)
	}
	assert.Equal(t, []uint64{3, 4, 5}, seqs, "resend starts right after the server's last ack: no gap, no acknowledged duplicate")
	assert.Equal(t, []string{"t3", "t4", "t5"}, toks)
	res := s2.expect(t, runneragent.KindResult)
	require.True(t, res.Result.OK)
	s2.none(t, runneragent.KindOutput, 100*time.Millisecond)

	s2.push(runneragent.Inbound{Kind: runneragent.KindAck, JobID: "job-1", Generation: 1, AckResult: true})
	require.Eventually(t, func() bool {
		p, _ := h.spool.Pending(context.Background())
		return len(p) == 0
	}, 5*time.Second, time.Millisecond)
}

func TestAgentTerminalResultReplayedUntilAcked(t *testing.T) {
	h := newHarness(t, 64)
	ref := runneragent.JobRef{JobID: "job-9", Generation: 4}
	h.start()
	s1 := h.admitted()
	s1.push(dispatch(ref, string(runner.CredAnthropicSubscription)))
	s1.expect(t, runneragent.KindAccept)
	s1.expect(t, runneragent.KindOutput)
	s1.expect(t, runneragent.KindResult) // server crashes before acknowledging

	mark := h.clock.mark()
	require.NoError(t, s1.Close())
	h.clock.awaitAndAdvance(t, mark, 500*time.Millisecond)
	s2 := h.session()
	hello := admit(t, s2, h.key.Public().(ed25519.PublicKey))
	assert.Equal(t, []runneragent.HeldJob{{JobID: "job-9", Generation: 4, State: runneragent.HeldTerminal, LastOutputSeq: 1}}, hello.Held)
	assert.Equal(t, uint64(1), s2.expect(t, runneragent.KindOutput).Seq)
	res := s2.expect(t, runneragent.KindResult)
	assert.Equal(t, "ok", res.Result.Chat.Output)
	s2.push(runneragent.Inbound{Kind: runneragent.KindAck, JobID: "job-9", Generation: 4, AckResult: true})
	require.Eventually(t, func() bool {
		p, _ := h.spool.Pending(context.Background())
		return len(p) == 0
	}, 5*time.Second, time.Millisecond)

	mark = h.clock.mark()
	require.NoError(t, s2.Close())
	h.clock.awaitAndAdvance(t, mark, 500*time.Millisecond)
	s3 := h.session()
	hello3 := admit(t, s3, h.key.Public().(ed25519.PublicKey))
	assert.Empty(t, hello3.Held, "an acknowledged result is never replayed")
	s3.none(t, runneragent.KindResult, 100*time.Millisecond)
}

func TestAgentRestartReportsSpooledResultAndPrunesOrphans(t *testing.T) {
	ctx := context.Background()
	spool := runneragent.NewMemorySpool(64)
	done := runneragent.JobRef{JobID: "job-done", Generation: 2}
	orphan := runneragent.JobRef{JobID: "job-orphan", Generation: 1}
	require.NoError(t, spool.AppendOutput(ctx, done, runneragent.OutputFrame{Seq: 1, Token: "a"}))
	require.NoError(t, spool.PutResult(ctx, done, runneragent.Result{OK: true, Chat: &runneragent.ChatResult{Output: "a"}}))
	require.NoError(t, spool.AppendOutput(ctx, orphan, runneragent.OutputFrame{Seq: 1, Token: "lost"}))

	h := newHarness(t, 64, withSpool(spool))
	h.start()
	s := h.session()
	hello := admit(t, s, h.key.Public().(ed25519.PublicKey))
	assert.Equal(t, []runneragent.HeldJob{{JobID: "job-done", Generation: 2, State: runneragent.HeldTerminal, LastOutputSeq: 1}}, hello.Held)
	assert.Equal(t, "a", s.expect(t, runneragent.KindOutput).Output.Token)
	s.expect(t, runneragent.KindResult)
	_, ok, err := spool.Get(ctx, orphan)
	require.NoError(t, err)
	assert.False(t, ok, "frames of a job that died with the previous process are pruned")
}

func TestAgentFencedKillsLocalProcessAndDropsJob(t *testing.T) {
	h := newHarness(t, 64)
	ref := runneragent.JobRef{JobID: "job-f", Generation: 1}
	killed := make(chan error, 1)
	h.exec.script(ref, func(ctx context.Context, d runneragent.Dispatch, emit runneragent.EmitFunc) runneragent.Result {
		_ = emit(ctx, "partial")
		<-ctx.Done()
		killed <- ctx.Err()
		return runneragent.Result{OK: true, Chat: &runneragent.ChatResult{Output: "late"}}
	})
	h.start()
	s := h.admitted()
	s.push(dispatch(ref, string(runner.CredAnthropicSubscription)))
	s.expect(t, runneragent.KindAccept)
	s.expect(t, runneragent.KindOutput)
	s.push(runneragent.Inbound{Kind: runneragent.KindFenced, JobID: "job-f", Generation: 1})
	select {
	case err := <-killed:
		assert.ErrorIs(t, err, context.Canceled, "fenced cancels the local process context")
	case <-time.After(5 * time.Second):
		t.Fatal("fenced job was not killed")
	}
	s.none(t, runneragent.KindResult, 150*time.Millisecond)
	require.Eventually(t, func() bool {
		p, _ := h.spool.Pending(context.Background())
		return len(p) == 0
	}, 5*time.Second, time.Millisecond)

	s.push(dispatch(ref, string(runner.CredAnthropicSubscription)))
	s.none(t, runneragent.KindAccept, 100*time.Millisecond)
	assert.Equal(t, 1, h.exec.callCount(), "a fenced generation is never restarted")
}

func TestAgentSpoolFullBlocksOutputNeverDrops(t *testing.T) {
	h := newHarness(t, 2)
	ref := runneragent.JobRef{JobID: "job-b", Generation: 1}
	var emitted atomic.Int32
	h.exec.script(ref, func(ctx context.Context, d runneragent.Dispatch, emit runneragent.EmitFunc) runneragent.Result {
		for i := 1; i <= 5; i++ {
			if err := emit(ctx, fmt.Sprintf("t%d", i)); err != nil {
				return runneragent.Result{ErrorText: err.Error()}
			}
			emitted.Add(1)
		}
		return runneragent.Result{OK: true, Chat: &runneragent.ChatResult{Output: "t1t2t3t4t5"}}
	})
	h.start()
	s := h.admitted()
	s.push(dispatch(ref, string(runner.CredAnthropicSubscription)))
	s.expect(t, runneragent.KindAccept)

	var toks []string
	toks = append(toks, s.expect(t, runneragent.KindOutput).Output.Token, s.expect(t, runneragent.KindOutput).Output.Token)
	require.Eventually(t, func() bool { return emitted.Load() == 2 }, 5*time.Second, time.Millisecond)
	require.Never(t, func() bool { return emitted.Load() > 2 }, 150*time.Millisecond, 10*time.Millisecond, "the third emit blocks on a full spool")

	s.push(runneragent.Inbound{Kind: runneragent.KindAck, JobID: "job-b", Generation: 1, Seq: 2})
	toks = append(toks, s.expect(t, runneragent.KindOutput).Output.Token, s.expect(t, runneragent.KindOutput).Output.Token)
	require.Eventually(t, func() bool { return emitted.Load() == 4 }, 5*time.Second, time.Millisecond)
	require.Never(t, func() bool { return emitted.Load() > 4 }, 150*time.Millisecond, 10*time.Millisecond)

	s.push(runneragent.Inbound{Kind: runneragent.KindAck, JobID: "job-b", Generation: 1, Seq: 4})
	toks = append(toks, s.expect(t, runneragent.KindOutput).Output.Token)
	s.expect(t, runneragent.KindResult)
	assert.Equal(t, []string{"t1", "t2", "t3", "t4", "t5"}, toks, "every frame arrives, in order, despite the bound")
}
